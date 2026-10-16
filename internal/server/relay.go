package server

import (
	"errors"
	"net"

	"p2pTunnel/internal/protocol"
)

func (s *Server) udpRelayLoop() {
	defer s.wg.Done()
	var buf [2048]byte

	for {
		n, remoteAddr, err := s.udpConn.ReadFromUDP(buf[:])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-s.closed:
				return
			default:
				continue
			}
		}

		pkt := buf[:n]

		// 1. Handle UDP Endpoint Discovery Probe ("P2PB")
		if roomToken, srcSlot, ok := protocol.DecodeProbePacket(pkt); ok {
			s.handleUDPProbe(roomToken, srcSlot, remoteAddr)
			continue
		}

		// 2. Handle Encrypted QUIC Relay Packet ("P2PR")
		if roomToken, srcSlot, dstSlot, _, ok := protocol.DecodeRelayPacket(pkt); ok {
			s.handleUDPRelay(roomToken, srcSlot, dstSlot, remoteAddr, pkt)
			continue
		}
	}
}

func (s *Server) handleUDPProbe(roomToken uint64, srcSlot uint8, remoteAddr *net.UDPAddr) {
	// Always reply with ProbeAck so the client learns its public IP:port.
	ack := protocol.EncodeProbeAckPacket(roomToken, remoteAddr.String())
	_, _ = s.udpConn.WriteToUDP(ack, remoteAddr)

	s.mu.Lock()
	rm, exists := s.roomsByToken[roomToken]
	if !exists {
		s.mu.Unlock()
		return
	}
	cp, exists := rm.peersBySlot[srcSlot]
	if !exists {
		s.mu.Unlock()
		return
	}

	changed := cp.setUDPAddr(remoteAddr)
	if !changed {
		s.mu.Unlock()
		return
	}

	peersSnapshot := snapshotRoomPeersLocked(rm)
	allPeers := make([]*connectedPeer, 0, len(rm.peersByID))
	otherPeers := make([]*connectedPeer, 0, len(rm.peersByID))
	for _, p := range rm.peersByID {
		allPeers = append(allPeers, p)
		if p.info.PeerID != cp.info.PeerID {
			otherPeers = append(otherPeers, p)
		}
	}
	selfInfo := cp.info
	roomCode := rm.code
	s.mu.Unlock()

	s.logger.Printf("[server] room %s peer %s (slot %d) discovered public UDP endpoint %s",
		roomCode, selfInfo.Name, srcSlot, remoteAddr.String())

	// Broadcast updated RoomState so all peers know the new PublicEndpoint.
	stateEnv, _ := protocol.NewEnvelope(protocol.MsgRoomState, protocol.RoomStateMsg{
		RoomCode: roomCode,
		Peers:    peersSnapshot,
	})
	for _, p := range allPeers {
		_ = p.send(stateEnv)
	}

	// Trigger simultaneous UDP hole punching between this peer and all other peers in the room.
	for _, other := range otherPeers {
		otherInfo := other.info
		envToSelf, _ := protocol.NewEnvelope(protocol.MsgStartPunch, protocol.StartPunchMsg{Peer: otherInfo})
		envToOther, _ := protocol.NewEnvelope(protocol.MsgStartPunch, protocol.StartPunchMsg{Peer: selfInfo})
		_ = cp.send(envToSelf)
		_ = other.send(envToOther)
	}
}

func (s *Server) handleUDPRelay(roomToken uint64, srcSlot, dstSlot uint8, remoteAddr *net.UDPAddr, rawPkt []byte) {
	s.mu.RLock()
	rm, exists := s.roomsByToken[roomToken]
	if !exists {
		s.mu.RUnlock()
		return
	}
	srcPeer := rm.peersBySlot[srcSlot]
	dstPeer := rm.peersBySlot[dstSlot]
	s.mu.RUnlock()

	if srcPeer == nil || dstPeer == nil {
		return
	}

	// Keep the source peer's UDP address fresh in case NAT re-mapped or probe was skipped.
	srcPeer.setUDPAddr(remoteAddr)

	dstAddr := dstPeer.getUDPAddr()
	if dstAddr == nil {
		return
	}

	_, _ = s.udpConn.WriteToUDP(rawPkt, dstAddr)
}
