package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"p2pTunnel/internal/crypto"
	"p2pTunnel/internal/protocol"
)

// TransportMode indicates how a peer connection is currently routed.
type TransportMode string

const (
	TransportConnecting TransportMode = "Connecting..."
	TransportDirect     TransportMode = "Direct P2P"
	TransportRelay      TransportMode = "EU Relay"
	TransportOffline    TransportMode = "Disconnected"
)

// peerTunnel holds the active QUIC connection(s) and transport metrics for a single room peer.
type peerTunnel struct {
	mu          sync.RWMutex
	info        protocol.PeerInfo
	activeConn  *quic.Conn
	mode        TransportMode
	rtt         time.Duration
	lastPunchAt time.Time
	punchedAddr *net.UDPAddr
	cancelDial  context.CancelFunc
}

func (pt *peerTunnel) getConn() (*quic.Conn, TransportMode) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.activeConn, pt.mode
}

func (pt *peerTunnel) setPunchedAddr(addr *net.UDPAddr) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.punchedAddr = addr
	pt.lastPunchAt = time.Now()
}

func (pt *peerTunnel) getPunchedAddr() *net.UDPAddr {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.punchedAddr
}

// defaultQUICConfig returns a tuned quic.Config suitable for both direct UDP and 14-byte relay-wrapped packets.
func defaultQUICConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:          45 * time.Second,
		KeepAlivePeriod:         10 * time.Second,
		MaxIncomingStreams:      4096,
		MaxIncomingUniStreams:   512,
		InitialPacketSize:       1240, // Leaves headroom for 14-byte P2PR relay header within 1280 MTU
		DisablePathMTUDiscovery: true,
	}
}

func (m *Manager) acceptQUICLoop(ctx context.Context) {
	defer m.wg.Done()
	for {
		conn, err := m.quicLn.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, quic.ErrServerClosed) {
				return
			}
			continue
		}
		m.wg.Add(1)
		go func(c *quic.Conn) {
			defer m.wg.Done()
			m.registerAcceptedQUICConn(ctx, c)
		}(conn)
	}
}

func (m *Manager) registerAcceptedQUICConn(ctx context.Context, conn *quic.Conn) {
	tlsState := conn.ConnectionState().TLS
	if len(tlsState.PeerCertificates) == 0 {
		_ = conn.CloseWithError(1, "missing client certificate")
		return
	}
	fp := crypto.CertFingerprint(tlsState.PeerCertificates[0].Raw)

	peerInfo, ok := m.findPeerByFingerprint(fp)
	if !ok {
		_ = conn.CloseWithError(2, "unknown room peer")
		return
	}

	mode := TransportDirect
	if _, isRelay := protocol.ParseRelayUDPAddr(conn.RemoteAddr()); isRelay {
		mode = TransportRelay
	}

	m.installPeerConn(ctx, peerInfo.PeerID, conn, mode)
}

func (m *Manager) installPeerConn(ctx context.Context, peerID string, conn *quic.Conn, mode TransportMode) {
	m.mu.Lock()
	pt, exists := m.peers[peerID]
	if !exists {
		m.mu.Unlock()
		_ = conn.CloseWithError(3, "peer no longer in room")
		return
	}

	pt.mu.Lock()
	oldConn := pt.activeConn
	oldMode := pt.mode

	// Keep an existing alive Direct P2P connection, and never replace an alive connection of the same or better tier.
	shouldReplace := true
	if oldConn != nil && oldConn.Context().Err() == nil {
		if oldMode == TransportDirect {
			shouldReplace = false
		} else if oldMode == TransportRelay && mode == TransportRelay {
			shouldReplace = false
		}
	}

	if shouldReplace {
		pt.activeConn = conn
		pt.mode = mode
	}
	peerName := pt.info.Name
	peerIP := pt.info.VirtualIP
	pt.mu.Unlock()
	m.mu.Unlock()

	if shouldReplace {
		m.logf("Connected to %s (%s) via %s [%s]", peerName, peerIP, mode, conn.RemoteAddr().String())
		m.notifyStateChange()
		if oldConn != nil && oldConn != conn && oldMode == TransportRelay && mode == TransportDirect {
			// Gracefully retire old relay connection after existing streams finish or after 5s.
			go func(retire *quic.Conn) {
				time.Sleep(5 * time.Second)
				_ = retire.CloseWithError(0, "upgraded to direct p2p")
			}(oldConn)
		}
	}

	// Measure initial RTT and start periodic ping + stream acceptance on this connection.
	m.wg.Add(2)
	go m.acceptPeerStreams(ctx, peerID, conn)
	go m.pingPeerLoop(ctx, peerID, conn)
}

// ensurePeerTunnel starts a background connector for a room peer if not already connected.
func (m *Manager) ensurePeerTunnel(ctx context.Context, peer protocol.PeerInfo, forcePunch bool) {
	if peer.PeerID == m.identity.PeerID {
		return
	}

	m.mu.Lock()
	pt, exists := m.peers[peer.PeerID]
	if !exists {
		pt = &peerTunnel{
			info: peer,
			mode: TransportConnecting,
		}
		m.peers[peer.PeerID] = pt
	} else {
		pt.mu.Lock()
		pt.info = peer
		pt.mu.Unlock()
	}
	selfSlot := m.selfInfo.Slot
	roomToken := m.roomToken
	m.mu.Unlock()

	// Always fire a burst of UDP hole-punch packets toward the peer's candidates when requested.
	if forcePunch || pt.getPunchedAddr() == nil {
		go m.sendPunchBurst(roomToken, selfSlot, peer)
	}

	pt.mu.Lock()
	hasAliveConn := pt.activeConn != nil && pt.activeConn.Context().Err() == nil
	isDirect := pt.mode == TransportDirect
	alreadyDialing := pt.cancelDial != nil
	if (hasAliveConn && isDirect) || alreadyDialing {
		pt.mu.Unlock()
		return
	}
	dialCtx, cancel := context.WithCancel(ctx)
	pt.cancelDial = cancel
	pt.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			pt.mu.Lock()
			pt.cancelDial = nil
			pt.mu.Unlock()
		}()
		m.connectPeerRoutine(dialCtx, pt, selfSlot)
	}()
}

func (m *Manager) sendPunchBurst(roomToken uint64, selfSlot uint8, peer protocol.PeerInfo) {
	if roomToken == 0 || selfSlot == 0 || m.cfg.ForceRelay {
		return
	}
	candidates := collectPeerCandidates(peer)
	if len(candidates) == 0 {
		return
	}
	pkt := protocol.EncodePunchPacket(roomToken, protocol.PunchKindPing, selfSlot)
	for i := 0; i < 6; i++ {
		for _, addr := range candidates {
			_ = m.muxConn.SendRaw(pkt, addr)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

func (m *Manager) connectPeerRoutine(ctx context.Context, pt *peerTunnel, selfSlot uint8) {
	pt.mu.RLock()
	peer := pt.info
	pt.mu.RUnlock()

	// Wait briefly until the peer has registered its UDP endpoint with the EU server.
	waitDeadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(waitDeadline) {
		pt.mu.RLock()
		pubEP := pt.info.PublicEndpoint
		peer = pt.info
		pt.mu.RUnlock()
		if pubEP != "" {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}

	// Give the lower-slot peer a head-start on dialing so both sides don't collide on the same UDP 4-tuple.
	if selfSlot > peer.Slot {
		waitDur := 800 * time.Millisecond
		if m.cfg.ForceRelay {
			waitDur = 1500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(waitDur):
		}
		if conn, _ := pt.getConn(); conn != nil && conn.Context().Err() == nil {
			return
		}
	}

	// Step 1: Attempt Direct P2P QUIC connection (unless ForceRelay is enabled).
	if !m.cfg.ForceRelay {
		if directConn := m.tryDialDirect(ctx, pt); directConn != nil {
			m.installPeerConn(m.roomCtx, peer.PeerID, directConn, TransportDirect)
			return
		}
	}

	// Check if incoming connection already succeeded while we were dialing.
	if conn, _ := pt.getConn(); conn != nil && conn.Context().Err() == nil {
		return
	}

	// Step 2: Fallback to EU Server Encrypted UDP Relay!
	pt.mu.RLock()
	peer = pt.info
	pt.mu.RUnlock()
	relayConn, err := m.tryDialRelay(ctx, peer)
	if err == nil && relayConn != nil {
		m.installPeerConn(m.roomCtx, peer.PeerID, relayConn, TransportRelay)
	}
}

func (m *Manager) tryDialDirect(ctx context.Context, pt *peerTunnel) *quic.Conn {
	pt.mu.RLock()
	peer := pt.info
	punched := pt.punchedAddr
	pt.mu.RUnlock()

	candidates := collectPeerCandidates(peer)
	if punched != nil {
		candidates = append([]*net.UDPAddr{punched}, candidates...)
	}
	if len(candidates) == 0 {
		// Wait briefly for UDP probe discovery or punch callback.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(900 * time.Millisecond):
		}
		pt.mu.RLock()
		peer = pt.info
		punched = pt.punchedAddr
		pt.mu.RUnlock()
		candidates = collectPeerCandidates(peer)
		if punched != nil {
			candidates = append([]*net.UDPAddr{punched}, candidates...)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	tlsCfg := m.identity.PeerClientTLSConfig(peer.CertFingerprint)
	seen := make(map[string]bool)

	for _, addr := range candidates {
		if ctx.Err() != nil {
			return nil
		}
		if addr == nil || seen[addr.String()] {
			continue
		}
		seen[addr.String()] = true

		// Stop dialing if an incoming Direct P2P connection was already accepted.
		if conn, mode := pt.getConn(); conn != nil && conn.Context().Err() == nil && mode == TransportDirect {
			return nil
		}

		attemptCtx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
		quicCfg := defaultQUICConfig()
		quicCfg.HandshakeIdleTimeout = 550 * time.Millisecond
		c, err := m.quicTr.Dial(attemptCtx, addr, tlsCfg, quicCfg)
		cancel()
		if err == nil && c != nil {
			return c
		}
	}
	return nil
}

func (m *Manager) tryDialRelay(ctx context.Context, peer protocol.PeerInfo) (*quic.Conn, error) {
	if peer.Slot == 0 {
		return nil, errors.New("invalid peer slot for relay")
	}
	relayCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	relayAddr := protocol.RelayUDPAddr(peer.Slot)
	tlsCfg := m.identity.PeerClientTLSConfig(peer.CertFingerprint)
	quicCfg := defaultQUICConfig()
	quicCfg.HandshakeIdleTimeout = 3500 * time.Millisecond

	return m.quicTr.Dial(relayCtx, relayAddr, tlsCfg, quicCfg)
}

func (m *Manager) pingPeerLoop(ctx context.Context, peerID string, conn *quic.Conn) {
	defer m.wg.Done()

	measure := func() bool {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		stream, err := conn.OpenStreamSync(pingCtx)
		if err != nil {
			return false
		}
		defer stream.Close()

		start := time.Now()
		var req [9]byte
		req[0] = protocol.StreamTypePing
		binary.BigEndian.PutUint64(req[1:9], uint64(start.UnixNano()))
		_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := stream.Write(req[:]); err != nil {
			return false
		}
		var resp [8]byte
		if _, err := io.ReadFull(stream, resp[:]); err != nil {
			return false
		}
		rtt := time.Since(start)

		m.mu.RLock()
		pt, ok := m.peers[peerID]
		m.mu.RUnlock()
		if ok {
			pt.mu.Lock()
			if pt.activeConn == conn {
				pt.rtt = rtt
			}
			pt.mu.Unlock()
			m.notifyStateChange()
		}
		return true
	}

	if !measure() {
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-conn.Context().Done():
			m.handleConnClosed(peerID, conn)
			return
		case <-ticker.C:
			if !measure() {
				if conn.Context().Err() != nil {
					m.handleConnClosed(peerID, conn)
					return
				}
			}
		}
	}
}

func (m *Manager) handleConnClosed(peerID string, closedConn *quic.Conn) {
	m.mu.RLock()
	pt, exists := m.peers[peerID]
	m.mu.RUnlock()
	if !exists {
		return
	}

	pt.mu.Lock()
	if pt.activeConn == closedConn {
		pt.activeConn = nil
		pt.mode = TransportConnecting
		peer := pt.info
		pt.mu.Unlock()
		m.notifyStateChange()
		if m.roomCtx != nil && m.roomCtx.Err() == nil {
			m.ensurePeerTunnel(m.roomCtx, peer, true)
		}
		return
	}
	pt.mu.Unlock()
}

func collectPeerCandidates(peer protocol.PeerInfo) []*net.UDPAddr {
	var out []*net.UDPAddr
	seen := make(map[string]bool)
	add := func(raw string) {
		if raw == "" || seen[raw] {
			return
		}
		if uAddr, err := net.ResolveUDPAddr("udp", raw); err == nil && uAddr.Port > 0 {
			seen[raw] = true
			out = append(out, uAddr)
		}
	}
	add(peer.PublicEndpoint)
	for _, cand := range peer.LocalCandidates {
		add(cand)
	}
	return out
}

// discoverLocalUDPCandidates gathers non-loopback IPv4 LAN addresses with the given local UDP port.
func discoverLocalUDPCandidates(udpPort int) []string {
	if udpPort <= 0 {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, fmt.Sprintf("%s:%d", ip4.String(), udpPort))
		}
	}
	return out
}
