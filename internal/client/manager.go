package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"p2pTunnel/internal/crypto"
	"p2pTunnel/internal/protocol"
)

// ManagerConfig configures a client Manager instance.
type ManagerConfig struct {
	ServerAddr    string
	ServerSecret  string
	PeerName      string
	ForceRelay    bool
	OnStateChange func(Snapshot)
}

// PeerStatus represents the UI-friendly state of a remote room peer.
type PeerStatus struct {
	PeerID         string
	Name           string
	Slot           uint8
	VirtualIP      string
	Mode           TransportMode
	RTT            time.Duration
	PublicEndpoint string
	SharedPorts    []protocol.PortShare
}

// LocalShareStatus represents a local port being shared by this client.
type LocalShareStatus struct {
	Protocol    string
	Port        uint16
	Name        string
	ActiveConns int32
	BytesTotal  uint64
}

// RemoteBindingStatus represents a remote peer's shared port mapped locally on this machine.
type RemoteBindingStatus struct {
	Key         string
	PeerID      string
	PeerName    string
	PeerIP      string
	Protocol    string
	RemotePort  uint16
	Name        string
	Enabled     bool
	BindIP      string
	LocalPort   uint16
	ActualBound string
	StatusErr   string
	ActiveConns int32
	BytesTotal  uint64
}

// Snapshot is an immutable point-in-time view of the client's state for the GUI or CLI.
type Snapshot struct {
	Connected      bool
	ServerAddr     string
	RoomCode       string
	SelfPeer       protocol.PeerInfo
	Peers          []PeerStatus
	LocalShares    []LocalShareStatus
	RemoteBindings []RemoteBindingStatus
	Logs           []string
}

type localSharedPort struct {
	share       protocol.PortShare
	activeConns atomic.Int32
	bytesTotal  atomic.Uint64
}

// Manager orchestrates signaling, peer-to-peer QUIC tunnels, and local TCP/UDP port mappings.
type Manager struct {
	cfg      ManagerConfig
	identity *crypto.Identity

	mu             sync.RWMutex
	connected      bool
	serverAddr     string
	serverSecret   string
	roomCode       string
	roomToken      uint64
	selfInfo       protocol.PeerInfo
	sigConn        net.Conn
	sigWriteMu     sync.Mutex
	rawUDP         *net.UDPConn
	muxConn        *protocol.MuxPacketConn
	quicTr         *quic.Transport
	quicLn         *quic.Listener
	roomCtx        context.Context
	roomCancel     context.CancelFunc
	peers          map[string]*peerTunnel
	localShares    map[string]*localSharedPort
	remoteBindings map[string]*remotePortBinding

	logMu sync.Mutex
	logs  []string

	wg sync.WaitGroup
}

// NewManager creates a new client Manager with a fresh ephemeral Ed25519 identity.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.ServerAddr == "" {
		cfg.ServerAddr = "127.0.0.1:4433"
	}
	if strings.TrimSpace(cfg.PeerName) == "" {
		cfg.PeerName = "Peer"
	}
	id, err := crypto.GenerateIdentity()
	if err != nil {
		return nil, err
	}
	m := &Manager{
		cfg:            cfg,
		identity:       id,
		serverAddr:     cfg.ServerAddr,
		serverSecret:   cfg.ServerSecret,
		peers:          make(map[string]*peerTunnel),
		localShares:    make(map[string]*localSharedPort),
		remoteBindings: make(map[string]*remotePortBinding),
	}
	m.logf("Initialized identity %s (fingerprint %s...)", id.PeerID, id.Fingerprint[:12])
	return m, nil
}

// Identity returns the client's ephemeral identity.
func (m *Manager) Identity() *crypto.Identity {
	return m.identity
}

// SetServerSecret updates the shared EU relay server secret used when creating or joining rooms.
func (m *Manager) SetServerSecret(secret string) {
	m.mu.Lock()
	m.serverSecret = secret
	m.mu.Unlock()
}

func (m *Manager) getServerSecret() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.serverSecret
}

// CreateRoom connects to the EU server and creates a new room (with optional custom code and password).
func (m *Manager) CreateRoom(ctx context.Context, serverAddr, peerName, customCode, password string) error {
	secretHash := crypto.HashServerSecret(m.getServerSecret())
	return m.connectRoom(ctx, serverAddr, peerName, func(localCands []string, shares []protocol.PortShare) (protocol.Envelope, error) {
		req := protocol.CreateRoomReq{
			ServerSecretHash: secretHash,
			CustomCode:       strings.TrimSpace(customCode),
			PasswordHash:     crypto.HashRoomPassword(password),
			PeerID:           m.identity.PeerID,
			PeerName:         peerName,
			CertFingerprint:  m.identity.Fingerprint,
			LocalCandidates:  localCands,
			SharedPorts:      shares,
		}
		return protocol.NewEnvelope(protocol.MsgCreateRoom, req)
	})
}

// JoinRoom connects to the EU server and joins an existing room using roomCode and optional password.
func (m *Manager) JoinRoom(ctx context.Context, serverAddr, peerName, roomCode, password string) error {
	roomCode = strings.ToUpper(strings.TrimSpace(roomCode))
	if roomCode == "" {
		return errors.New("room code cannot be empty")
	}
	secretHash := crypto.HashServerSecret(m.getServerSecret())
	return m.connectRoom(ctx, serverAddr, peerName, func(localCands []string, shares []protocol.PortShare) (protocol.Envelope, error) {
		req := protocol.JoinRoomReq{
			ServerSecretHash: secretHash,
			RoomCode:         roomCode,
			PasswordHash:     crypto.HashRoomPassword(password),
			PeerID:           m.identity.PeerID,
			PeerName:         peerName,
			CertFingerprint:  m.identity.Fingerprint,
			LocalCandidates:  localCands,
			SharedPorts:      shares,
		}
		return protocol.NewEnvelope(protocol.MsgJoinRoom, req)
	})
}

func (m *Manager) connectRoom(
	ctx context.Context,
	serverAddr string,
	peerName string,
	buildHandshake func(localCands []string, shares []protocol.PortShare) (protocol.Envelope, error),
) error {
	m.LeaveRoom()

	serverAddr = strings.TrimSpace(serverAddr)
	if serverAddr == "" {
		serverAddr = m.cfg.ServerAddr
	}
	peerName = strings.TrimSpace(peerName)
	if peerName == "" {
		peerName = m.cfg.PeerName
	}

	// 1. Bind local UDP socket for QUIC P2P + STUN probe + EU Relay.
	rawUDP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("bind local udp socket: %w", err)
	}
	localUDPPort := rawUDP.LocalAddr().(*net.UDPAddr).Port
	localCands := discoverLocalUDPCandidates(localUDPPort)

	muxConn := protocol.NewMuxPacketConn(rawUDP)
	quicTr := &quic.Transport{Conn: muxConn}
	quicLn, err := quicTr.Listen(m.identity.PeerServerTLSConfig(m.isAllowedPeerFingerprint), defaultQUICConfig())
	if err != nil {
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("start quic listener: %w", err)
	}

	// 2. Dial EU Signaling Server over TLS 1.3.
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	sigConn, err := tls.DialWithDialer(dialer, "tcp", serverAddr, m.identity.SignalingClientTLSConfig())
	if err != nil {
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("connect to EU server (%s): %w", serverAddr, err)
	}

	shares := m.collectLocalShareSpecs()
	handshakeEnv, err := buildHandshake(localCands, shares)
	if err != nil {
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return err
	}

	_ = sigConn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := protocol.WriteSignalingMsg(sigConn, handshakeEnv); err != nil {
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("send room handshake: %w", err)
	}

	respEnv, err := protocol.ReadSignalingMsg(sigConn)
	_ = sigConn.SetDeadline(time.Time{})
	if err != nil {
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("read room handshake response: %w", err)
	}

	if respEnv.Type == protocol.MsgError {
		var errMsg protocol.ErrorMsg
		_ = respEnv.Decode(&errMsg)
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return errors.New(errMsg.Message)
	}
	if respEnv.Type != protocol.MsgRoomJoined {
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("unexpected server response: %s", respEnv.Type)
	}

	var joined protocol.RoomJoinedResp
	if err := respEnv.Decode(&joined); err != nil {
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("decode room_joined: %w", err)
	}

	srvHost, _, err := net.SplitHostPort(serverAddr)
	if err != nil {
		srvHost = serverAddr
	}
	serverUDPAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", srvHost, joined.ServerUDPPort))
	if err != nil {
		_ = sigConn.Close()
		_ = quicLn.Close()
		_ = quicTr.Close()
		_ = rawUDP.Close()
		return fmt.Errorf("resolve server UDP address: %w", err)
	}

	roomCtx, roomCancel := context.WithCancel(context.Background())

	muxConn.ConfigureRoom(
		joined.RoomToken,
		joined.Self.Slot,
		serverUDPAddr,
		m.onProbeAck,
		m.onPunchReceived,
	)

	m.mu.Lock()
	m.connected = true
	m.serverAddr = serverAddr
	m.roomCode = joined.RoomCode
	m.roomToken = joined.RoomToken
	m.selfInfo = joined.Self
	m.sigConn = sigConn
	m.rawUDP = rawUDP
	m.muxConn = muxConn
	m.quicTr = quicTr
	m.quicLn = quicLn
	m.roomCtx = roomCtx
	m.roomCancel = roomCancel
	m.peers = make(map[string]*peerTunnel)
	m.remoteBindings = make(map[string]*remotePortBinding)
	m.mu.Unlock()

	m.logf("Joined room %s — Assigned Room IP: %s (Slot %d)",
		joined.RoomCode, joined.Self.VirtualIP, joined.Self.Slot)

	m.wg.Add(3)
	go m.acceptQUICLoop(roomCtx)
	go m.signalingReadLoop(roomCtx, sigConn)
	go m.udpProbeKeepaliveLoop(roomCtx, serverUDPAddr, joined.RoomToken, joined.Self.Slot)

	m.syncRoomPeers(joined.Peers)
	m.notifyStateChange()
	return nil
}

// LeaveRoom disconnects from the current room and stops all active peer tunnels and port listeners.
func (m *Manager) LeaveRoom() {
	m.mu.Lock()
	if !m.connected && m.roomCancel == nil {
		m.mu.Unlock()
		return
	}
	roomCode := m.roomCode
	m.connected = false
	m.roomCode = ""
	m.roomToken = 0
	m.selfInfo = protocol.PeerInfo{}

	if m.roomCancel != nil {
		m.roomCancel()
		m.roomCancel = nil
	}
	if m.sigConn != nil {
		_ = m.sigConn.Close()
		m.sigConn = nil
	}
	for _, rb := range m.remoteBindings {
		rb.stop()
	}
	m.remoteBindings = make(map[string]*remotePortBinding)

	for _, pt := range m.peers {
		pt.mu.Lock()
		if pt.cancelDial != nil {
			pt.cancelDial()
		}
		if pt.activeConn != nil {
			_ = pt.activeConn.CloseWithError(0, "leaving room")
		}
		pt.mu.Unlock()
	}
	m.peers = make(map[string]*peerTunnel)

	if m.quicLn != nil {
		_ = m.quicLn.Close()
		m.quicLn = nil
	}
	if m.quicTr != nil {
		_ = m.quicTr.Close()
		m.quicTr = nil
	}
	if m.rawUDP != nil {
		_ = m.rawUDP.Close()
		m.rawUDP = nil
	}
	m.mu.Unlock()

	m.wg.Wait()
	if roomCode != "" {
		m.logf("Left room %s", roomCode)
	}
	m.notifyStateChange()
}

// Close shuts down the manager completely.
func (m *Manager) Close() error {
	m.LeaveRoom()
	return nil
}

// AddSharedPort adds a local TCP, UDP, or Both ("both") port to the room share list.
func (m *Manager) AddSharedPort(proto string, port uint16, name string) error {
	if port == 0 {
		return errors.New("port must be between 1 and 65535")
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	name = strings.TrimSpace(name)

	var protos []string
	switch proto {
	case "tcp":
		protos = []string{"tcp"}
	case "udp":
		protos = []string{"udp"}
	case "both", "tcp+udp":
		protos = []string{"tcp", "udp"}
	default:
		return fmt.Errorf("unsupported protocol %q (use tcp, udp, or both)", proto)
	}

	m.mu.Lock()
	for _, p := range protos {
		share := protocol.PortShare{
			Protocol: p,
			Port:     port,
			Name:     name,
		}
		key := share.Key()
		m.localShares[key] = &localSharedPort{share: share}
	}
	connected := m.connected
	m.mu.Unlock()

	m.logf("Shared local %s port %d (%s)", strings.ToUpper(proto), port, name)
	if connected {
		m.pushSharedPortsUpdate()
	}
	m.notifyStateChange()
	return nil
}

// RemoveSharedPort stops sharing a local port with the room.
func (m *Manager) RemoveSharedPort(proto string, port uint16) {
	proto = strings.ToLower(strings.TrimSpace(proto))
	key := fmt.Sprintf("%s:%d", proto, port)

	m.mu.Lock()
	_, existed := m.localShares[key]
	delete(m.localShares, key)
	connected := m.connected
	m.mu.Unlock()

	if existed {
		m.logf("Stopped sharing local %s port %d", strings.ToUpper(proto), port)
		if connected {
			m.pushSharedPortsUpdate()
		}
		m.notifyStateChange()
	}
}

// SetRemotePortEnabled enables or disables a remote peer's shared port on this machine.
func (m *Manager) SetRemotePortEnabled(key string, enabled bool) {
	m.mu.RLock()
	rb, ok := m.remoteBindings[key]
	roomCtx := m.roomCtx
	m.mu.RUnlock()
	if !ok {
		return
	}

	rb.mu.Lock()
	rb.enabled = enabled
	rb.mu.Unlock()

	if enabled && roomCtx != nil {
		m.startRemoteBinding(roomCtx, rb)
	} else {
		rb.stop()
		m.logf("Disabled remote port %s:%d from %s", strings.ToUpper(rb.share.Protocol), rb.share.Port, rb.peerName)
	}
	m.notifyStateChange()
}

// SetRemotePortOverride overrides the local bind IP and/or port for a remote peer's shared port.
func (m *Manager) SetRemotePortOverride(key string, bindIP string, localPort uint16) error {
	bindIP = strings.TrimSpace(bindIP)
	if bindIP != "" && net.ParseIP(bindIP) == nil {
		return fmt.Errorf("invalid bind IP address %q", bindIP)
	}

	m.mu.RLock()
	rb, ok := m.remoteBindings[key]
	roomCtx := m.roomCtx
	m.mu.RUnlock()
	if !ok {
		return errors.New("remote port binding not found")
	}

	rb.mu.Lock()
	if bindIP == "" {
		rb.bindIP = rb.peerIP
	} else {
		rb.bindIP = bindIP
	}
	if localPort == 0 {
		rb.localPort = rb.share.Port
	} else {
		rb.localPort = localPort
	}
	enabled := rb.enabled
	rb.mu.Unlock()

	if enabled && roomCtx != nil {
		m.startRemoteBinding(roomCtx, rb)
	}
	m.notifyStateChange()
	return nil
}

func (m *Manager) pushSharedPortsUpdate() {
	shares := m.collectLocalShareSpecs()
	env, err := protocol.NewEnvelope(protocol.MsgUpdatePorts, protocol.UpdatePortsReq{
		SharedPorts: shares,
	})
	if err != nil {
		return
	}
	_ = m.sendSignaling(env)
}

func (m *Manager) collectLocalShareSpecs() []protocol.PortShare {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]protocol.PortShare, 0, len(m.localShares))
	for _, ls := range m.localShares {
		out = append(out, ls.share)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out
}

func (m *Manager) getLocalShare(proto string, port uint16) *localSharedPort {
	key := fmt.Sprintf("%s:%d", strings.ToLower(proto), port)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.localShares[key]
}

func (m *Manager) sendSignaling(env protocol.Envelope) error {
	m.mu.RLock()
	conn := m.sigConn
	m.mu.RUnlock()
	if conn == nil {
		return errors.New("not connected to signaling server")
	}
	m.sigWriteMu.Lock()
	defer m.sigWriteMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	return protocol.WriteSignalingMsg(conn, env)
}

func (m *Manager) signalingReadLoop(ctx context.Context, conn net.Conn) {
	defer m.wg.Done()

	// Periodic ping goroutine to keep the signaling TCP connection alive.
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				env, _ := protocol.NewEnvelope(protocol.MsgPing, protocol.PingPongMsg{Timestamp: time.Now().UnixNano()})
				_ = m.sendSignaling(env)
			}
		}
	}()

	for {
		env, err := protocol.ReadSignalingMsg(conn)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				m.logf("Disconnected from EU signaling server: %v", err)
				go m.LeaveRoom()
				return
			}
		}

		switch env.Type {
		case protocol.MsgRoomState:
			var state protocol.RoomStateMsg
			if err := env.Decode(&state); err == nil {
				m.syncRoomPeers(state.Peers)
				m.notifyStateChange()
			}

		case protocol.MsgStartPunch:
			var sp protocol.StartPunchMsg
			if err := env.Decode(&sp); err == nil {
				m.ensurePeerTunnel(ctx, sp.Peer, true)
			}

		case protocol.MsgError:
			var em protocol.ErrorMsg
			if err := env.Decode(&em); err == nil {
				m.logf("Server error: %s", em.Message)
				m.notifyStateChange()
			}
		}
	}
}

func (m *Manager) udpProbeKeepaliveLoop(ctx context.Context, serverUDPAddr *net.UDPAddr, roomToken uint64, selfSlot uint8) {
	defer m.wg.Done()
	pkt := protocol.EncodeProbePacket(roomToken, selfSlot)

	// Send initial burst so the EU server immediately learns our external UDP mapping.
	for i := 0; i < 3; i++ {
		if m.muxConn != nil {
			_ = m.muxConn.SendRaw(pkt, serverUDPAddr)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(120 * time.Millisecond):
		}
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.muxConn != nil {
				_ = m.muxConn.SendRaw(pkt, serverUDPAddr)
			}
		}
	}
}

func (m *Manager) onProbeAck(publicEndpoint string) {
	m.mu.Lock()
	changed := m.selfInfo.PublicEndpoint != publicEndpoint
	m.selfInfo.PublicEndpoint = publicEndpoint
	m.mu.Unlock()
	if changed {
		m.logf("Discovered public UDP endpoint: %s", publicEndpoint)
		m.notifyStateChange()
	}
}

func (m *Manager) onPunchReceived(peerSlot uint8, addr *net.UDPAddr) {
	m.mu.RLock()
	var target *peerTunnel
	for _, pt := range m.peers {
		pt.mu.RLock()
		slot := pt.info.Slot
		pt.mu.RUnlock()
		if slot == peerSlot {
			target = pt
			break
		}
	}
	roomCtx := m.roomCtx
	m.mu.RUnlock()

	if target == nil || roomCtx == nil {
		return
	}
	target.setPunchedAddr(addr)
	if conn, mode := target.getConn(); conn == nil || conn.Context().Err() != nil || mode != TransportDirect {
		target.mu.RLock()
		info := target.info
		target.mu.RUnlock()
		m.ensurePeerTunnel(roomCtx, info, false)
	}
}

func (m *Manager) syncRoomPeers(peers []protocol.PeerInfo) {
	m.mu.Lock()
	roomCtx := m.roomCtx
	if roomCtx == nil {
		m.mu.Unlock()
		return
	}

	activePeerIDs := make(map[string]protocol.PeerInfo, len(peers))
	for _, p := range peers {
		if p.PeerID == m.identity.PeerID {
			m.selfInfo = p
			continue
		}
		activePeerIDs[p.PeerID] = p
	}

	// Remove peers that left the room.
	for id, pt := range m.peers {
		if _, stillHere := activePeerIDs[id]; !stillHere {
			pt.mu.Lock()
			peerName := pt.info.Name
			if pt.cancelDial != nil {
				pt.cancelDial()
			}
			if pt.activeConn != nil {
				_ = pt.activeConn.CloseWithError(0, "peer left room")
			}
			pt.mu.Unlock()
			delete(m.peers, id)
			m.logf("Peer %s left the room", peerName)
		}
	}

	// Reconcile remote port bindings across all active peers.
	desiredBindings := make(map[string]struct {
		peer  protocol.PeerInfo
		share protocol.PortShare
	})
	for _, p := range activePeerIDs {
		for _, sh := range p.SharedPorts {
			bKey := fmt.Sprintf("%s:%s:%d", p.PeerID, strings.ToLower(sh.Protocol), sh.Port)
			desiredBindings[bKey] = struct {
				peer  protocol.PeerInfo
				share protocol.PortShare
			}{peer: p, share: sh}
		}
	}

	for bKey, rb := range m.remoteBindings {
		if _, keep := desiredBindings[bKey]; !keep {
			rb.stop()
			delete(m.remoteBindings, bKey)
		}
	}

	var bindingsToStart []*remotePortBinding
	for bKey, item := range desiredBindings {
		if existing, exists := m.remoteBindings[bKey]; exists {
			existing.mu.Lock()
			existing.peerName = item.peer.Name
			existing.share.Name = item.share.Name
			existing.mu.Unlock()
			continue
		}
		rb := &remotePortBinding{
			key:       bKey,
			peerID:    item.peer.PeerID,
			peerName:  item.peer.Name,
			peerIP:    item.peer.VirtualIP,
			share:     item.share,
			enabled:   true,
			bindIP:    item.peer.VirtualIP,
			localPort: item.share.Port,
		}
		m.remoteBindings[bKey] = rb
		bindingsToStart = append(bindingsToStart, rb)
	}
	m.mu.Unlock()

	for _, p := range activePeerIDs {
		m.ensurePeerTunnel(roomCtx, p, false)
	}
	for _, rb := range bindingsToStart {
		m.startRemoteBinding(roomCtx, rb)
	}
}

func (m *Manager) isAllowedPeerFingerprint(fp string) bool {
	_, ok := m.findPeerByFingerprint(fp)
	return ok
}

func (m *Manager) findPeerByFingerprint(fp string) (protocol.PeerInfo, bool) {
	fp = strings.ToLower(strings.TrimSpace(fp))
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, pt := range m.peers {
		pt.mu.RLock()
		info := pt.info
		pt.mu.RUnlock()
		if strings.ToLower(info.CertFingerprint) == fp {
			return info, true
		}
	}
	return protocol.PeerInfo{}, false
}

// Snapshot returns a thread-safe snapshot of the current client state.
func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	snap := Snapshot{
		Connected:  m.connected,
		ServerAddr: m.serverAddr,
		RoomCode:   m.roomCode,
		SelfPeer:   m.selfInfo,
	}

	for _, pt := range m.peers {
		pt.mu.RLock()
		ps := PeerStatus{
			PeerID:         pt.info.PeerID,
			Name:           pt.info.Name,
			Slot:           pt.info.Slot,
			VirtualIP:      pt.info.VirtualIP,
			Mode:           pt.mode,
			RTT:            pt.rtt,
			PublicEndpoint: pt.info.PublicEndpoint,
			SharedPorts:    append([]protocol.PortShare(nil), pt.info.SharedPorts...),
		}
		pt.mu.RUnlock()
		snap.Peers = append(snap.Peers, ps)
	}

	for _, ls := range m.localShares {
		snap.LocalShares = append(snap.LocalShares, LocalShareStatus{
			Protocol:    ls.share.Protocol,
			Port:        ls.share.Port,
			Name:        ls.share.Name,
			ActiveConns: ls.activeConns.Load(),
			BytesTotal:  ls.bytesTotal.Load(),
		})
	}

	for _, rb := range m.remoteBindings {
		rb.mu.Lock()
		rbs := RemoteBindingStatus{
			Key:         rb.key,
			PeerID:      rb.peerID,
			PeerName:    rb.peerName,
			PeerIP:      rb.peerIP,
			Protocol:    rb.share.Protocol,
			RemotePort:  rb.share.Port,
			Name:        rb.share.Name,
			Enabled:     rb.enabled,
			BindIP:      rb.bindIP,
			LocalPort:   rb.localPort,
			ActualBound: rb.actualBound,
			StatusErr:   rb.statusErr,
			ActiveConns: rb.activeConns.Load(),
			BytesTotal:  rb.bytesTotal.Load(),
		}
		rb.mu.Unlock()
		snap.RemoteBindings = append(snap.RemoteBindings, rbs)
	}
	m.mu.RUnlock()

	sort.Slice(snap.Peers, func(i, j int) bool {
		return snap.Peers[i].Slot < snap.Peers[j].Slot
	})
	sort.Slice(snap.LocalShares, func(i, j int) bool {
		if snap.LocalShares[i].Port != snap.LocalShares[j].Port {
			return snap.LocalShares[i].Port < snap.LocalShares[j].Port
		}
		return snap.LocalShares[i].Protocol < snap.LocalShares[j].Protocol
	})
	sort.Slice(snap.RemoteBindings, func(i, j int) bool {
		if snap.RemoteBindings[i].PeerIP != snap.RemoteBindings[j].PeerIP {
			return snap.RemoteBindings[i].PeerIP < snap.RemoteBindings[j].PeerIP
		}
		if snap.RemoteBindings[i].RemotePort != snap.RemoteBindings[j].RemotePort {
			return snap.RemoteBindings[i].RemotePort < snap.RemoteBindings[j].RemotePort
		}
		return snap.RemoteBindings[i].Protocol < snap.RemoteBindings[j].Protocol
	})

	m.logMu.Lock()
	snap.Logs = append([]string(nil), m.logs...)
	m.logMu.Unlock()

	return snap
}

func (m *Manager) logf(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	m.logMu.Lock()
	m.logs = append(m.logs, line)
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
	m.logMu.Unlock()
}

func (m *Manager) notifyStateChange() {
	if m.cfg.OnStateChange != nil {
		snap := m.Snapshot()
		m.cfg.OnStateChange(snap)
	}
}
