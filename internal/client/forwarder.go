package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"p2pTunnel/internal/protocol"
)

// countingWriter wraps an io.Writer and atomically adds written bytes to counter.
type countingWriter struct {
	w       io.Writer
	counter * atomic.Uint64
}

func (cw countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 && cw.counter != nil {
		cw.counter.Add(uint64(n))
	}
	return n, err
}

func (m *Manager) acceptPeerStreams(ctx context.Context, peerID string, conn *quic.Conn) {
	defer m.wg.Done()
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		m.wg.Add(1)
		go func(s *quic.Stream) {
			defer m.wg.Done()
			m.handleIncomingStream(ctx, peerID, s)
		}(stream)
	}
}

func (m *Manager) handleIncomingStream(ctx context.Context, peerID string, stream *quic.Stream) {
	defer stream.Close()

	var typeBuf [1]byte
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(stream, typeBuf[:]); err != nil {
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	switch typeBuf[0] {
	case protocol.StreamTypePing:
		var tsBuf [8]byte
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(stream, tsBuf[:]); err != nil {
			return
		}
		_, _ = stream.Write(tsBuf[:])

	case protocol.StreamTypeTCP:
		m.handleIncomingTCPStream(ctx, peerID, stream)

	case protocol.StreamTypeUDP:
		m.handleIncomingUDPStream(ctx, peerID, stream)
	}
}

func (m *Manager) handleIncomingTCPStream(ctx context.Context, peerID string, stream *quic.Stream) {
	port, err := protocol.ReadStreamOpenPort(stream)
	if err != nil {
		return
	}

	localShare := m.getLocalShare("tcp", port)
	if localShare == nil {
		_ = protocol.WriteStreamStatus(stream, protocol.StreamStatusErr, fmt.Sprintf("TCP port %d is not shared", port))
		m.logf("Blocked unshared TCP port %d request from peer %s", port, peerID)
		return
	}

	targetAddr := fmt.Sprintf("127.0.0.1:%d", port)
	dialer := net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		_ = protocol.WriteStreamStatus(stream, protocol.StreamStatusErr, fmt.Sprintf("local service %s unreachable: %v", targetAddr, err))
		return
	}
	defer localConn.Close()

	if err := protocol.WriteStreamStatus(stream, protocol.StreamStatusOK, ""); err != nil {
		return
	}

	localShare.activeConns.Add(1)
	m.notifyStateChange()
	defer func() {
		localShare.activeConns.Add(-1)
		m.notifyStateChange()
	}()

	pipeBidirectional(stream, localConn, &localShare.bytesTotal)
}

func (m *Manager) handleIncomingUDPStream(ctx context.Context, peerID string, stream *quic.Stream) {
	port, err := protocol.ReadStreamOpenPort(stream)
	if err != nil {
		return
	}

	localShare := m.getLocalShare("udp", port)
	if localShare == nil {
		_ = protocol.WriteStreamStatus(stream, protocol.StreamStatusErr, fmt.Sprintf("UDP port %d is not shared", port))
		m.logf("Blocked unshared UDP port %d request from peer %s", port, peerID)
		return
	}

	targetAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		_ = protocol.WriteStreamStatus(stream, protocol.StreamStatusErr, err.Error())
		return
	}
	udpConn, err := net.DialUDP("udp", nil, targetAddr)
	if err != nil {
		_ = protocol.WriteStreamStatus(stream, protocol.StreamStatusErr, err.Error())
		return
	}
	defer udpConn.Close()

	if err := protocol.WriteStreamStatus(stream, protocol.StreamStatusOK, ""); err != nil {
		return
	}

	localShare.activeConns.Add(1)
	m.notifyStateChange()
	defer func() {
		localShare.activeConns.Add(-1)
		m.notifyStateChange()
	}()

	done := make(chan struct{})
	// Local UDP socket -> QUIC Stream
	go func() {
		defer close(done)
		var buf [65535]byte
		for {
			_ = udpConn.SetReadDeadline(time.Now().Add(60 * time.Second))
			n, err := udpConn.Read(buf[:])
			if err != nil {
				return
			}
			localShare.bytesTotal.Add(uint64(n))
			if err := protocol.WriteUDPFrame(stream, buf[:n]); err != nil {
				return
			}
		}
	}()

	// QUIC Stream -> Local UDP socket
	var buf [65535]byte
	for {
		_ = stream.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := protocol.ReadUDPFrame(stream, buf[:])
		if err != nil {
			break
		}
		localShare.bytesTotal.Add(uint64(n))
		if _, err := udpConn.Write(buf[:n]); err != nil {
			break
		}
	}
	_ = udpConn.Close()
	<-done
}

func pipeBidirectional(a io.ReadWriteCloser, b io.ReadWriteCloser, byteCounter *atomic.Uint64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(countingWriter{w: a, counter: byteCounter}, b)
		_ = a.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(countingWriter{w: b, counter: byteCounter}, a)
		_ = b.Close()
	}()
	wg.Wait()
}

// remotePortBinding manages a local TCP or UDP listener (on 127.0.0.x:port) that forwards traffic to a remote peer.
type remotePortBinding struct {
	key         string // "<peerID>:<protocol>:<remotePort>"
	peerID      string
	peerName    string
	peerIP      string
	share       protocol.PortShare
	enabled     bool
	bindIP      string
	localPort   uint16
	actualBound string
	statusErr   string

	activeConns atomic.Int32
	bytesTotal  atomic.Uint64

	mu      sync.Mutex
	tcpLn   net.Listener
	udpConn *net.UDPConn
	cancel  context.CancelFunc
}

func (rb *remotePortBinding) stop() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.cancel != nil {
		rb.cancel()
		rb.cancel = nil
	}
	if rb.tcpLn != nil {
		_ = rb.tcpLn.Close()
		rb.tcpLn = nil
	}
	if rb.udpConn != nil {
		_ = rb.udpConn.Close()
		rb.udpConn = nil
	}
	rb.actualBound = ""
}

func (m *Manager) startRemoteBinding(ctx context.Context, rb *remotePortBinding) {
	rb.stop()

	rb.mu.Lock()
	if !rb.enabled {
		rb.statusErr = ""
		rb.mu.Unlock()
		return
	}
	bindIP := rb.bindIP
	if bindIP == "" {
		bindIP = rb.peerIP
	}
	localPort := rb.localPort
	if localPort == 0 {
		localPort = rb.share.Port
	}
	proto := strings.ToLower(rb.share.Protocol)
	bindCtx, cancel := context.WithCancel(ctx)
	rb.cancel = cancel
	rb.mu.Unlock()

	switch proto {
	case "tcp":
		ln, boundAddr, err := listenTCPWithFallback(bindIP, localPort)
		rb.mu.Lock()
		if err != nil {
			rb.statusErr = err.Error()
			rb.mu.Unlock()
			m.logf("Failed to bind remote TCP port %s:%d: %v", bindIP, localPort, err)
			return
		}
		rb.tcpLn = ln
		rb.actualBound = boundAddr
		rb.statusErr = ""
		rb.mu.Unlock()

		m.logf("Mapped %s's TCP port %d (%s) -> %s", rb.peerName, rb.share.Port, rb.share.Name, boundAddr)
		m.wg.Add(1)
		go m.serveRemoteTCP(bindCtx, rb, ln)

	case "udp":
		uConn, boundAddr, err := listenUDPWithFallback(bindIP, localPort)
		rb.mu.Lock()
		if err != nil {
			rb.statusErr = err.Error()
			rb.mu.Unlock()
			m.logf("Failed to bind remote UDP port %s:%d: %v", bindIP, localPort, err)
			return
		}
		rb.udpConn = uConn
		rb.actualBound = boundAddr
		rb.statusErr = ""
		rb.mu.Unlock()

		m.logf("Mapped %s's UDP port %d (%s) -> %s", rb.peerName, rb.share.Port, rb.share.Name, boundAddr)
		m.wg.Add(1)
		go m.serveRemoteUDP(bindCtx, rb, uConn)
	}
}

func listenTCPWithFallback(bindIP string, preferredPort uint16) (net.Listener, string, error) {
	primary := fmt.Sprintf("%s:%d", bindIP, preferredPort)
	ln, err := net.Listen("tcp", primary)
	if err == nil {
		return ln, ln.Addr().String(), nil
	}
	// If privileged port (<1024) or busy, try fallback port (preferredPort + 10000).
	if preferredPort < 55000 {
		fallback := fmt.Sprintf("%s:%d", bindIP, preferredPort+10000)
		if ln2, err2 := net.Listen("tcp", fallback); err2 == nil {
			return ln2, ln2.Addr().String(), nil
		}
	}
	return nil, "", err
}

func listenUDPWithFallback(bindIP string, preferredPort uint16) (*net.UDPConn, string, error) {
	primary := fmt.Sprintf("%s:%d", bindIP, preferredPort)
	if uAddr, err := net.ResolveUDPAddr("udp", primary); err == nil {
		if uConn, err := net.ListenUDP("udp", uAddr); err == nil {
			return uConn, uConn.LocalAddr().String(), nil
		}
	}
	if preferredPort < 55000 {
		fallback := fmt.Sprintf("%s:%d", bindIP, preferredPort+10000)
		if uAddr, err := net.ResolveUDPAddr("udp", fallback); err == nil {
			if uConn, err := net.ListenUDP("udp", uAddr); err == nil {
				return uConn, uConn.LocalAddr().String(), nil
			}
		}
	}
	return nil, "", fmt.Errorf("unable to bind UDP on %s", primary)
}

func (m *Manager) serveRemoteTCP(ctx context.Context, rb *remotePortBinding, ln net.Listener) {
	defer m.wg.Done()
	for {
		clientConn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		m.wg.Add(1)
		go func(c net.Conn) {
			defer m.wg.Done()
			m.forwardRemoteTCPConn(ctx, rb, c)
		}(clientConn)
	}
}

func (m *Manager) forwardRemoteTCPConn(ctx context.Context, rb *remotePortBinding, clientConn net.Conn) {
	defer clientConn.Close()

	quicConn, err := m.waitForPeerConn(ctx, rb.peerID, 6*time.Second)
	if err != nil {
		m.logf("Cannot forward TCP %d to %s: %v", rb.share.Port, rb.peerName, err)
		return
	}

	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	stream, err := quicConn.OpenStreamSync(openCtx)
	cancel()
	if err != nil {
		m.logf("Failed to open QUIC stream to %s: %v", rb.peerName, err)
		return
	}
	defer stream.Close()

	if err := protocol.WriteStreamOpenHeader(stream, protocol.StreamTypeTCP, rb.share.Port); err != nil {
		return
	}
	if err := protocol.ReadStreamStatus(stream); err != nil {
		m.logf("Remote TCP %d on %s rejected: %v", rb.share.Port, rb.peerName, err)
		return
	}

	rb.activeConns.Add(1)
	m.notifyStateChange()
	defer func() {
		rb.activeConns.Add(-1)
		m.notifyStateChange()
	}()

	pipeBidirectional(clientConn, stream, &rb.bytesTotal)
}

type udpClientSession struct {
	stream     *quic.Stream
	lastActive atomic.Int64
}

func (m *Manager) serveRemoteUDP(ctx context.Context, rb *remotePortBinding, uConn *net.UDPConn) {
	defer m.wg.Done()

	var (
		sessMu   sync.Mutex
		sessions = make(map[string]*udpClientSession)
	)

	defer func() {
		sessMu.Lock()
		for _, s := range sessions {
			_ = s.stream.Close()
		}
		sessMu.Unlock()
	}()

	var buf [65535]byte
	for {
		n, clientAddr, err := uConn.ReadFromUDP(buf[:])
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		payload := append([]byte(nil), buf[:n]...)
		addrKey := clientAddr.String()

		sessMu.Lock()
		sess, exists := sessions[addrKey]
		sessMu.Unlock()

		if !exists {
			quicConn, err := m.waitForPeerConn(ctx, rb.peerID, 5*time.Second)
			if err != nil {
				continue
			}
			openCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			stream, err := quicConn.OpenStreamSync(openCtx)
			cancel()
			if err != nil {
				continue
			}
			if err := protocol.WriteStreamOpenHeader(stream, protocol.StreamTypeUDP, rb.share.Port); err != nil {
				_ = stream.Close()
				continue
			}
			if err := protocol.ReadStreamStatus(stream); err != nil {
				_ = stream.Close()
				continue
			}

			sess = &udpClientSession{stream: stream}
			sess.lastActive.Store(time.Now().UnixNano())

			sessMu.Lock()
			sessions[addrKey] = sess
			sessMu.Unlock()

			rb.activeConns.Add(1)
			m.notifyStateChange()

			// Reader goroutine: QUIC Stream -> Local UDP Client
			go func(cAddr *net.UDPAddr, key string, st *quic.Stream) {
				defer func() {
					_ = st.Close()
					sessMu.Lock()
					if sessions[key] == sess {
						delete(sessions, key)
					}
					sessMu.Unlock()
					rb.activeConns.Add(-1)
					m.notifyStateChange()
				}()
				var rbuf [65535]byte
				for {
					_ = st.SetReadDeadline(time.Now().Add(60 * time.Second))
					rn, err := protocol.ReadUDPFrame(st, rbuf[:])
					if err != nil {
						return
					}
					sess.lastActive.Store(time.Now().UnixNano())
					rb.bytesTotal.Add(uint64(rn))
					_, _ = uConn.WriteToUDP(rbuf[:rn], cAddr)
				}
			}(clientAddr, addrKey, stream)
		}

		sess.lastActive.Store(time.Now().UnixNano())
		rb.bytesTotal.Add(uint64(len(payload)))
		if err := protocol.WriteUDPFrame(sess.stream, payload); err != nil {
			sessMu.Lock()
			if sessions[addrKey] == sess {
				delete(sessions, addrKey)
			}
			sessMu.Unlock()
			_ = sess.stream.Close()
		}
	}
}

func (m *Manager) waitForPeerConn(ctx context.Context, peerID string, timeout time.Duration) (*quic.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		m.mu.RLock()
		pt, ok := m.peers[peerID]
		m.mu.RUnlock()
		if !ok {
			return nil, errors.New("peer not in room")
		}
		conn, _ := pt.getConn()
		if conn != nil && conn.Context().Err() == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for peer tunnel connection")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
	}
}
