package server

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"p2pTunnel/internal/crypto"
	"p2pTunnel/internal/protocol"
)

const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// Config configures the EU signaling and UDP relay server.
type Config struct {
	ListenAddr   string // e.g. "0.0.0.0:4433" (used for both TLS/TCP signaling and UDP relay unless UDPAddr is set)
	UDPAddr      string // optional override for UDP bind address; defaults to ListenAddr
	ServerSecret string // optional shared secret required to connect to this relay server
	Logger       *log.Logger
}

type connectedPeer struct {
	info    protocol.PeerInfo
	conn    net.Conn
	writeMu sync.Mutex
	udpMu   sync.RWMutex
	udpAddr *net.UDPAddr
}

func (cp *connectedPeer) send(env protocol.Envelope) error {
	cp.writeMu.Lock()
	defer cp.writeMu.Unlock()
	_ = cp.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return protocol.WriteSignalingMsg(cp.conn, env)
}

func (cp *connectedPeer) setUDPAddr(addr *net.UDPAddr) (changed bool) {
	cp.udpMu.Lock()
	defer cp.udpMu.Unlock()
	ep := addr.String()
	if cp.info.PublicEndpoint != ep {
		cp.info.PublicEndpoint = ep
		changed = true
	}
	cp.udpAddr = addr
	return changed
}

func (cp *connectedPeer) getUDPAddr() *net.UDPAddr {
	cp.udpMu.RLock()
	defer cp.udpMu.RUnlock()
	return cp.udpAddr
}

type room struct {
	code         string
	token        uint64
	passwordHash string
	peersByID    map[string]*connectedPeer
	peersBySlot  map[uint8]*connectedPeer
}

// Server implements the EU room signaling coordinator, STUN-like UDP endpoint reflector,
// and encrypted QUIC packet relay.
type Server struct {
	cfg        Config
	identity   *crypto.Identity
	secretHash string
	logger     *log.Logger

	tcpLn   net.Listener
	udpConn *net.UDPConn

	mu           sync.RWMutex
	roomsByCode  map[string]*room
	roomsByToken map[uint64]*room

	closed chan struct{}
	wg     sync.WaitGroup
}

// New creates and initializes a new EU Server instance.
func New(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":4433"
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	id, err := crypto.GenerateIdentity()
	if err != nil {
		return nil, fmt.Errorf("generate server identity: %w", err)
	}
	return &Server{
		cfg:          cfg,
		identity:     id,
		secretHash:   crypto.HashServerSecret(cfg.ServerSecret),
		logger:       cfg.Logger,
		roomsByCode:  make(map[string]*room),
		roomsByToken: make(map[uint64]*room),
		closed:       make(chan struct{}),
	}, nil
}

// Start binds the TLS signaling listener and UDP relay socket and begins serving in the background.
func (s *Server) Start() error {
	tlsCfg := s.identity.SignalingServerTLSConfig()
	tcpLn, err := tls.Listen("tcp", s.cfg.ListenAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("listen tls %s: %w", s.cfg.ListenAddr, err)
	}
	s.tcpLn = tcpLn

	udpBind := s.cfg.UDPAddr
	if udpBind == "" {
		// If ListenAddr used port 0, bind UDP to the same port if possible, or ephemeral if busy.
		udpBind = s.cfg.ListenAddr
		if _, portStr, err := net.SplitHostPort(s.cfg.ListenAddr); err == nil && portStr == "0" {
			udpBind = "127.0.0.1:0"
		}
	}
	udpAddr, err := net.ResolveUDPAddr("udp", udpBind)
	if err != nil {
		_ = tcpLn.Close()
		return fmt.Errorf("resolve udp %s: %w", udpBind, err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		_ = tcpLn.Close()
		return fmt.Errorf("listen udp %s: %w", udpBind, err)
	}
	s.udpConn = udpConn

	s.logger.Printf("[server] listening signaling TLS on %s, UDP reflector/relay on %s",
		s.tcpLn.Addr().String(), s.udpConn.LocalAddr().String())

	s.wg.Add(2)
	go s.acceptLoop()
	go s.udpRelayLoop()
	return nil
}

// SignalingAddr returns the bound TCP/TLS listener address.
func (s *Server) SignalingAddr() string {
	if s.tcpLn == nil {
		return ""
	}
	return s.tcpLn.Addr().String()
}

// UDPPort returns the bound UDP reflector/relay port.
func (s *Server) UDPPort() int {
	if s.udpConn == nil {
		return 0
	}
	if uAddr, ok := s.udpConn.LocalAddr().(*net.UDPAddr); ok {
		return uAddr.Port
	}
	return 0
}

// Close shuts down the server and all active connections.
func (s *Server) Close() error {
	select {
	case <-s.closed:
		return nil
	default:
		close(s.closed)
	}
	if s.tcpLn != nil {
		_ = s.tcpLn.Close()
	}
	if s.udpConn != nil {
		_ = s.udpConn.Close()
	}
	s.mu.Lock()
	for _, r := range s.roomsByCode {
		for _, p := range r.peersByID {
			_ = p.conn.Close()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.tcpLn.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				s.logger.Printf("[server] accept error: %v", err)
				continue
			}
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleClient(c)
		}(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()

	var (
		activeRoom *room
		activePeer *connectedPeer
	)
	defer func() {
		if activeRoom != nil && activePeer != nil {
			s.removePeerFromRoom(activeRoom, activePeer.info.PeerID)
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		env, err := protocol.ReadSignalingMsg(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				select {
				case <-s.closed:
				default:
					// Normal disconnect or timeout
				}
			}
			return
		}

		switch env.Type {
		case protocol.MsgPing:
			var ping protocol.PingPongMsg
			_ = env.Decode(&ping)
			pongEnv, _ := protocol.NewEnvelope(protocol.MsgPong, protocol.PingPongMsg{Timestamp: ping.Timestamp})
			if activePeer != nil {
				_ = activePeer.send(pongEnv)
			} else {
				_ = protocol.WriteSignalingMsg(conn, pongEnv)
			}

		case protocol.MsgCreateRoom:
			if activeRoom != nil {
				s.sendError(conn, activePeer, "already in a room")
				continue
			}
			var req protocol.CreateRoomReq
			if err := env.Decode(&req); err != nil {
				s.sendError(conn, nil, "invalid create_room payload")
				continue
			}
			rm, cp, err := s.createRoom(conn, req)
			if err != nil {
				s.sendError(conn, nil, err.Error())
				continue
			}
			activeRoom = rm
			activePeer = cp

		case protocol.MsgJoinRoom:
			if activeRoom != nil {
				s.sendError(conn, activePeer, "already in a room")
				continue
			}
			var req protocol.JoinRoomReq
			if err := env.Decode(&req); err != nil {
				s.sendError(conn, nil, "invalid join_room payload")
				continue
			}
			rm, cp, err := s.joinRoom(conn, req)
			if err != nil {
				s.sendError(conn, nil, err.Error())
				continue
			}
			activeRoom = rm
			activePeer = cp

		case protocol.MsgUpdatePorts:
			if activeRoom == nil || activePeer == nil {
				s.sendError(conn, nil, "not in a room")
				continue
			}
			var req protocol.UpdatePortsReq
			if err := env.Decode(&req); err != nil {
				s.sendError(conn, activePeer, "invalid update_ports payload")
				continue
			}
			s.updatePeerPorts(activeRoom, activePeer.info.PeerID, req.SharedPorts)

		case protocol.MsgUpdateCandidates:
			if activeRoom == nil || activePeer == nil {
				continue
			}
			var req protocol.UpdateCandidatesReq
			if err := env.Decode(&req); err == nil {
				s.updatePeerCandidates(activeRoom, activePeer.info.PeerID, req.LocalCandidates)
			}

		case protocol.MsgRequestPunch:
			if activeRoom == nil || activePeer == nil {
				continue
			}
			var req protocol.RequestPunchReq
			if err := env.Decode(&req); err == nil {
				s.coordinatePunch(activeRoom, activePeer.info.PeerID, req.TargetPeerID)
			}
		}
	}
}

func (s *Server) sendError(conn net.Conn, cp *connectedPeer, msg string) {
	env, _ := protocol.NewEnvelope(protocol.MsgError, protocol.ErrorMsg{Message: msg})
	if cp != nil {
		_ = cp.send(env)
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = protocol.WriteSignalingMsg(conn, env)
}

func (s *Server) verifyServerSecret(clientHash string) error {
	if s.secretHash == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(s.secretHash), []byte(strings.ToLower(strings.TrimSpace(clientHash)))) != 1 {
		return errors.New("invalid relay server secret")
	}
	return nil
}

func (s *Server) createRoom(conn net.Conn, req protocol.CreateRoomReq) (*room, *connectedPeer, error) {
	if err := s.verifyServerSecret(req.ServerSecretHash); err != nil {
		return nil, nil, err
	}
	if req.PeerID == "" || req.CertFingerprint == "" {
		return nil, nil, errors.New("peer_id and cert_fingerprint are required")
	}

	s.mu.Lock()
	var code string
	if custom := normalizeRoomCode(req.CustomCode); custom != "" {
		if len(custom) < 3 || len(custom) > 32 {
			s.mu.Unlock()
			return nil, nil, errors.New("custom room code must be between 3 and 32 characters")
		}
		if _, exists := s.roomsByCode[custom]; exists {
			s.mu.Unlock()
			return nil, nil, fmt.Errorf("room %q is already in use", custom)
		}
		code = custom
	} else {
		var err error
		code, err = s.generateUniqueCodeLocked()
		if err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
	}

	token, err := s.generateUniqueTokenLocked()
	if err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}

	slot := uint8(1)
	peerName := strings.TrimSpace(req.PeerName)
	if peerName == "" {
		peerName = "Peer-1"
	}

	cp := &connectedPeer{
		conn: conn,
		info: protocol.PeerInfo{
			PeerID:          req.PeerID,
			Name:            peerName,
			Slot:            slot,
			VirtualIP:       protocol.SlotToVirtualIP(slot),
			CertFingerprint: strings.ToLower(strings.TrimSpace(req.CertFingerprint)),
			LocalCandidates: req.LocalCandidates,
			SharedPorts:     req.SharedPorts,
		},
	}

	rm := &room{
		code:         code,
		token:        token,
		passwordHash: req.PasswordHash,
		peersByID:    map[string]*connectedPeer{req.PeerID: cp},
		peersBySlot:  map[uint8]*connectedPeer{slot: cp},
	}
	s.roomsByCode[code] = rm
	s.roomsByToken[token] = rm
	peersSnapshot := snapshotRoomPeersLocked(rm)
	selfInfo := cp.info
	s.mu.Unlock()

	s.logger.Printf("[server] room %s created by %s (%s, slot %d -> %s)",
		code, peerName, req.PeerID, slot, selfInfo.VirtualIP)

	resp := protocol.RoomJoinedResp{
		RoomCode:      code,
		RoomToken:     token,
		Self:          selfInfo,
		Peers:         peersSnapshot,
		ServerUDPPort: s.UDPPort(),
	}
	env, _ := protocol.NewEnvelope(protocol.MsgRoomJoined, resp)
	if err := cp.send(env); err != nil {
		return nil, nil, err
	}
	return rm, cp, nil
}

func (s *Server) joinRoom(conn net.Conn, req protocol.JoinRoomReq) (*room, *connectedPeer, error) {
	if err := s.verifyServerSecret(req.ServerSecretHash); err != nil {
		return nil, nil, err
	}
	code := normalizeRoomCode(req.RoomCode)
	if code == "" {
		return nil, nil, errors.New("room code is required")
	}
	if req.PeerID == "" || req.CertFingerprint == "" {
		return nil, nil, errors.New("peer_id and cert_fingerprint are required")
	}

	s.mu.Lock()
	rm, exists := s.roomsByCode[code]
	if !exists {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("room %q not found", code)
	}
	if rm.passwordHash != "" && rm.passwordHash != req.PasswordHash {
		s.mu.Unlock()
		return nil, nil, errors.New("invalid room password")
	}

	var assignedSlot uint8
	for candidate := uint8(1); candidate <= 250; candidate++ {
		if _, busy := rm.peersBySlot[candidate]; !busy {
			assignedSlot = candidate
			break
		}
	}
	if assignedSlot == 0 {
		s.mu.Unlock()
		return nil, nil, errors.New("room is full")
	}

	peerName := strings.TrimSpace(req.PeerName)
	if peerName == "" {
		peerName = fmt.Sprintf("Peer-%d", assignedSlot)
	}

	cp := &connectedPeer{
		conn: conn,
		info: protocol.PeerInfo{
			PeerID:          req.PeerID,
			Name:            peerName,
			Slot:            assignedSlot,
			VirtualIP:       protocol.SlotToVirtualIP(assignedSlot),
			CertFingerprint: strings.ToLower(strings.TrimSpace(req.CertFingerprint)),
			LocalCandidates: req.LocalCandidates,
			SharedPorts:     req.SharedPorts,
		},
	}

	rm.peersByID[req.PeerID] = cp
	rm.peersBySlot[assignedSlot] = cp

	peersSnapshot := snapshotRoomPeersLocked(rm)
	otherPeers := make([]*connectedPeer, 0, len(rm.peersByID))
	for id, peer := range rm.peersByID {
		if id != req.PeerID {
			otherPeers = append(otherPeers, peer)
		}
	}
	selfInfo := cp.info
	token := rm.token
	s.mu.Unlock()

	s.logger.Printf("[server] peer %s (%s) joined room %s (slot %d -> %s)",
		peerName, req.PeerID, code, assignedSlot, selfInfo.VirtualIP)

	resp := protocol.RoomJoinedResp{
		RoomCode:      code,
		RoomToken:     token,
		Self:          selfInfo,
		Peers:         peersSnapshot,
		ServerUDPPort: s.UDPPort(),
	}
	joinedEnv, _ := protocol.NewEnvelope(protocol.MsgRoomJoined, resp)
	if err := cp.send(joinedEnv); err != nil {
		return nil, nil, err
	}

	stateEnv, _ := protocol.NewEnvelope(protocol.MsgRoomState, protocol.RoomStateMsg{
		RoomCode: code,
		Peers:    peersSnapshot,
	})
	for _, peer := range otherPeers {
		_ = peer.send(stateEnv)
	}

	return rm, cp, nil
}

func (s *Server) updatePeerPorts(rm *room, peerID string, sharedPorts []protocol.PortShare) {
	s.mu.Lock()
	cp, ok := rm.peersByID[peerID]
	if !ok {
		s.mu.Unlock()
		return
	}
	cp.info.SharedPorts = sharedPorts
	peersSnapshot := snapshotRoomPeersLocked(rm)
	recipients := make([]*connectedPeer, 0, len(rm.peersByID))
	for _, p := range rm.peersByID {
		recipients = append(recipients, p)
	}
	code := rm.code
	s.mu.Unlock()

	stateEnv, _ := protocol.NewEnvelope(protocol.MsgRoomState, protocol.RoomStateMsg{
		RoomCode: code,
		Peers:    peersSnapshot,
	})
	for _, p := range recipients {
		_ = p.send(stateEnv)
	}
}

func (s *Server) updatePeerCandidates(rm *room, peerID string, candidates []string) {
	s.mu.Lock()
	cp, ok := rm.peersByID[peerID]
	if !ok {
		s.mu.Unlock()
		return
	}
	cp.info.LocalCandidates = candidates
	peersSnapshot := snapshotRoomPeersLocked(rm)
	recipients := make([]*connectedPeer, 0, len(rm.peersByID))
	for _, p := range rm.peersByID {
		recipients = append(recipients, p)
	}
	code := rm.code
	s.mu.Unlock()

	stateEnv, _ := protocol.NewEnvelope(protocol.MsgRoomState, protocol.RoomStateMsg{
		RoomCode: code,
		Peers:    peersSnapshot,
	})
	for _, p := range recipients {
		_ = p.send(stateEnv)
	}
}

func (s *Server) coordinatePunch(rm *room, requesterID, targetID string) {
	s.mu.RLock()
	reqPeer := rm.peersByID[requesterID]
	tgtPeer := rm.peersByID[targetID]
	if reqPeer == nil || tgtPeer == nil {
		s.mu.RUnlock()
		return
	}
	reqInfo := reqPeer.info
	tgtInfo := tgtPeer.info
	s.mu.RUnlock()

	envToReq, _ := protocol.NewEnvelope(protocol.MsgStartPunch, protocol.StartPunchMsg{Peer: tgtInfo})
	envToTgt, _ := protocol.NewEnvelope(protocol.MsgStartPunch, protocol.StartPunchMsg{Peer: reqInfo})
	_ = reqPeer.send(envToReq)
	_ = tgtPeer.send(envToTgt)
}

func (s *Server) removePeerFromRoom(rm *room, peerID string) {
	s.mu.Lock()
	cp, exists := rm.peersByID[peerID]
	if !exists {
		s.mu.Unlock()
		return
	}
	delete(rm.peersByID, peerID)
	delete(rm.peersBySlot, cp.info.Slot)

	if len(rm.peersByID) == 0 {
		delete(s.roomsByCode, rm.code)
		delete(s.roomsByToken, rm.token)
		s.mu.Unlock()
		s.logger.Printf("[server] room %s closed (empty)", rm.code)
		return
	}

	peersSnapshot := snapshotRoomPeersLocked(rm)
	recipients := make([]*connectedPeer, 0, len(rm.peersByID))
	for _, p := range rm.peersByID {
		recipients = append(recipients, p)
	}
	code := rm.code
	s.mu.Unlock()

	s.logger.Printf("[server] peer %s left room %s", peerID, code)
	stateEnv, _ := protocol.NewEnvelope(protocol.MsgRoomState, protocol.RoomStateMsg{
		RoomCode: code,
		Peers:    peersSnapshot,
	})
	for _, p := range recipients {
		_ = p.send(stateEnv)
	}
}

func snapshotRoomPeersLocked(rm *room) []protocol.PeerInfo {
	out := make([]protocol.PeerInfo, 0, len(rm.peersByID))
	for _, p := range rm.peersByID {
		out = append(out, p.info)
	}
	return out
}

func normalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func (s *Server) generateUniqueCodeLocked() (string, error) {
	var b [6]byte
	for attempt := 0; attempt < 32; attempt++ {
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		var sb strings.Builder
		for i := 0; i < 6; i++ {
			if i == 3 {
				sb.WriteByte('-')
			}
			sb.WriteByte(codeAlphabet[int(b[i])%len(codeAlphabet)])
		}
		code := sb.String()
		if _, exists := s.roomsByCode[code]; !exists {
			return code, nil
		}
	}
	return "", errors.New("failed to generate unique room code")
}

func (s *Server) generateUniqueTokenLocked() (uint64, error) {
	var b [8]byte
	for attempt := 0; attempt < 32; attempt++ {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		tok := binary.BigEndian.Uint64(b[:])
		if tok == 0 {
			continue
		}
		if _, exists := s.roomsByToken[tok]; !exists {
			return tok, nil
		}
	}
	return 0, errors.New("failed to generate unique room token")
}
