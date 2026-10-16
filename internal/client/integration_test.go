package client_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"p2pTunnel/internal/client"
	"p2pTunnel/internal/protocol"
	"p2pTunnel/internal/server"
)

const testServerSecret = "eu-relay-secret-token"

func startTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv, err := server.New(server.Config{
		ListenAddr:   "127.0.0.1:0",
		UDPAddr:      "127.0.0.1:0",
		ServerSecret: testServerSecret,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
	})
	return srv
}

func startEchoTCPServer(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp echo: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

func startEchoUDPServer(t *testing.T) uint16 {
	t.Helper()
	uAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	uConn, err := net.ListenUDP("udp", uAddr)
	if err != nil {
		t.Fatalf("listen udp echo: %v", err)
	}
	t.Cleanup(func() { _ = uConn.Close() })

	go func() {
		var buf [4096]byte
		for {
			n, addr, err := uConn.ReadFromUDP(buf[:])
			if err != nil {
				return
			}
			reply := append([]byte("udp-echo:"), buf[:n]...)
			_, _ = uConn.WriteToUDP(reply, addr)
		}
	}()

	return uint16(uConn.LocalAddr().(*net.UDPAddr).Port)
}

func waitForCondition(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

func TestDirectP2PTCPAndUDPPortSharing(t *testing.T) {
	srv := startTestServer(t)
	tcpEchoPort := startEchoTCPServer(t)
	udpEchoPort := startEchoUDPServer(t)

	mgrA, err := client.NewManager(client.ManagerConfig{
		ServerAddr:   srv.SignalingAddr(),
		ServerSecret: "wrong-server-secret",
		PeerName:     "Alice",
	})
	if err != nil {
		t.Fatalf("NewManager Alice: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := client.NewManager(client.ManagerConfig{
		ServerAddr:   srv.SignalingAddr(),
		ServerSecret: testServerSecret,
		PeerName:     "Bob",
	})
	if err != nil {
		t.Fatalf("NewManager Bob: %v", err)
	}
	defer mgrB.Close()

	// Alice shares TCP echo port and UDP echo port before creating room.
	if err := mgrA.AddSharedPort("tcp", tcpEchoPort, "TCP Echo"); err != nil {
		t.Fatalf("AddSharedPort TCP: %v", err)
	}
	if err := mgrA.AddSharedPort("udp", udpEchoPort, "UDP Echo"); err != nil {
		t.Fatalf("AddSharedPort UDP: %v", err)
	}

	ctx := context.Background()
	// Verify wrong relay server secret is rejected.
	if err := mgrA.CreateRoom(ctx, srv.SignalingAddr(), "Alice", "", "secret123"); err == nil {
		t.Fatal("expected CreateRoom with wrong server secret to fail")
	}

	// Set correct relay server secret on Alice.
	mgrA.SetServerSecret(testServerSecret)
	if err := mgrA.CreateRoom(ctx, srv.SignalingAddr(), "Alice", "", "secret123"); err != nil {
		t.Fatalf("Alice CreateRoom: %v", err)
	}

	snapA := mgrA.Snapshot()
	if snapA.RoomCode == "" {
		t.Fatal("expected non-empty room code")
	}
	if snapA.SelfPeer.VirtualIP != "127.0.0.2" {
		t.Fatalf("expected Alice VirtualIP 127.0.0.2, got %s", snapA.SelfPeer.VirtualIP)
	}

	// Verify wrong password is rejected.
	if err := mgrB.JoinRoom(ctx, srv.SignalingAddr(), "Bob", snapA.RoomCode, "wrongpass"); err == nil {
		t.Fatal("expected JoinRoom with wrong password to fail")
	}

	// Bob joins with the correct code and password.
	if err := mgrB.JoinRoom(ctx, srv.SignalingAddr(), "Bob", snapA.RoomCode, "secret123"); err != nil {
		t.Fatalf("Bob JoinRoom: %v", err)
	}

	// Wait until Bob establishes a Direct P2P tunnel to Alice and maps both ports.
	var tcpBoundAddr, udpBoundAddr string
	waitForCondition(t, 8*time.Second, "Direct P2P connection and remote port mappings on Bob", func() bool {
		sb := mgrB.Snapshot()
		if len(sb.Peers) != 1 || sb.Peers[0].Mode != client.TransportDirect {
			return false
		}
		for _, rb := range sb.RemoteBindings {
			if rb.Protocol == "tcp" && rb.RemotePort == tcpEchoPort && rb.ActualBound != "" {
				tcpBoundAddr = rb.ActualBound
			}
			if rb.Protocol == "udp" && rb.RemotePort == udpEchoPort && rb.ActualBound != "" {
				udpBoundAddr = rb.ActualBound
			}
		}
		return tcpBoundAddr != "" && udpBoundAddr != ""
	})

	// Verify TCP bound address uses Alice's assigned 127.0.0.2 IP!
	expectedTCPAddr := fmt.Sprintf("127.0.0.2:%d", tcpEchoPort)
	if tcpBoundAddr != expectedTCPAddr {
		t.Fatalf("expected mapped TCP address %s, got %s", expectedTCPAddr, tcpBoundAddr)
	}

	// Test TCP traffic over the P2P tunnel.
	tcpConn, err := net.DialTimeout("tcp", tcpBoundAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial mapped TCP %s: %v", tcpBoundAddr, err)
	}
	defer tcpConn.Close()

	payload := []byte("hello over direct p2p quic tunnel!")
	if _, err := tcpConn.Write(payload); err != nil {
		t.Fatalf("write tcp: %v", err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(tcpConn, reply); err != nil {
		t.Fatalf("read tcp: %v", err)
	}
	if !bytes.Equal(payload, reply) {
		t.Fatalf("tcp reply mismatch: got %q, want %q", reply, payload)
	}

	// Test UDP traffic over the P2P tunnel.
	uTarget, _ := net.ResolveUDPAddr("udp", udpBoundAddr)
	uClient, err := net.DialUDP("udp", nil, uTarget)
	if err != nil {
		t.Fatalf("dial mapped UDP %s: %v", udpBoundAddr, err)
	}
	defer uClient.Close()

	_ = uClient.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := uClient.Write([]byte("game-packet-42")); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	var uBuf [256]byte
	un, err := uClient.Read(uBuf[:])
	if err != nil {
		t.Fatalf("read udp: %v", err)
	}
	if string(uBuf[:un]) != "udp-echo:game-packet-42" {
		t.Fatalf("unexpected udp response: %q", string(uBuf[:un]))
	}
}

func TestEURelayFallbackAndBidirectionalSharing(t *testing.T) {
	srv := startTestServer(t)
	aliceTCPPort := startEchoTCPServer(t)
	bobUDPPort := startEchoUDPServer(t)

	// Force both peers onto the EU Relay to simulate strict Symmetric NAT / CGNAT.
	mgrA, err := client.NewManager(client.ManagerConfig{
		ServerAddr:   srv.SignalingAddr(),
		ServerSecret: testServerSecret,
		PeerName:     "Alice",
		ForceRelay:   true,
	})
	if err != nil {
		t.Fatalf("NewManager Alice: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := client.NewManager(client.ManagerConfig{
		ServerAddr:   srv.SignalingAddr(),
		ServerSecret: testServerSecret,
		PeerName:     "Bob",
		ForceRelay:   true,
	})
	if err != nil {
		t.Fatalf("NewManager Bob: %v", err)
	}
	defer mgrB.Close()

	ctx := context.Background()
	if err := mgrA.CreateRoom(ctx, srv.SignalingAddr(), "Alice", "MY-ROOM", ""); err != nil {
		t.Fatalf("Alice CreateRoom: %v", err)
	}
	if err := mgrB.JoinRoom(ctx, srv.SignalingAddr(), "Bob", "my-room", ""); err != nil {
		t.Fatalf("Bob JoinRoom: %v", err)
	}

	// Both Alice and Bob share a port dynamically after connecting.
	if err := mgrA.AddSharedPort("tcp", aliceTCPPort, "Alice Web"); err != nil {
		t.Fatalf("Alice AddSharedPort: %v", err)
	}
	if err := mgrB.AddSharedPort("udp", bobUDPPort, "Bob Game"); err != nil {
		t.Fatalf("Bob AddSharedPort: %v", err)
	}

	var bobMappedAliceTCP string
	var aliceMappedBobUDP string

	waitForCondition(t, 8*time.Second, "EU Relay connection and bidirectional port mappings", func() bool {
		sa := mgrA.Snapshot()
		sb := mgrB.Snapshot()
		if len(sa.Peers) != 1 || sa.Peers[0].Mode != client.TransportRelay {
			return false
		}
		if len(sb.Peers) != 1 || sb.Peers[0].Mode != client.TransportRelay {
			return false
		}
		for _, rb := range sb.RemoteBindings {
			if rb.RemotePort == aliceTCPPort && rb.ActualBound != "" {
				bobMappedAliceTCP = rb.ActualBound
			}
		}
		for _, rb := range sa.RemoteBindings {
			if rb.RemotePort == bobUDPPort && rb.ActualBound != "" {
				aliceMappedBobUDP = rb.ActualBound
			}
		}
		return bobMappedAliceTCP != "" && aliceMappedBobUDP != ""
	})

	// 1. Bob accesses Alice's TCP port over the EU Relay (at 127.0.0.2:aliceTCPPort).
	conn, err := net.DialTimeout("tcp", bobMappedAliceTCP, 3*time.Second)
	if err != nil {
		t.Fatalf("Bob dial relayed TCP: %v", err)
	}
	defer conn.Close()
	msg := []byte("relayed over EU server with E2E TLS 1.3!")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write relayed tcp: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read relayed tcp: %v", err)
	}
	if !bytes.Equal(msg, got) {
		t.Fatalf("relayed tcp mismatch: got %q, want %q", got, msg)
	}

	// 2. Alice accesses Bob's UDP port over the EU Relay (at 127.0.0.3:bobUDPPort).
	uAddr, _ := net.ResolveUDPAddr("udp", aliceMappedBobUDP)
	uConn, err := net.DialUDP("udp", nil, uAddr)
	if err != nil {
		t.Fatalf("Alice dial relayed UDP: %v", err)
	}
	defer uConn.Close()
	_ = uConn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := uConn.Write([]byte("ping-bob")); err != nil {
		t.Fatalf("write relayed udp: %v", err)
	}
	var buf [128]byte
	n, err := uConn.Read(buf[:])
	if err != nil {
		t.Fatalf("read relayed udp: %v", err)
	}
	if string(buf[:n]) != "udp-echo:ping-bob" {
		t.Fatalf("relayed udp mismatch: %q", string(buf[:n]))
	}

	// 3. Verify Alice removing her shared port immediately prevents further access.
	mgrA.RemoveSharedPort("tcp", aliceTCPPort)
	waitForCondition(t, 4*time.Second, "Bob remote binding removed after Alice stops sharing", func() bool {
		return len(mgrB.Snapshot().RemoteBindings) == 0
	})
	_ = protocol.SlotToVirtualIP(1)
}
