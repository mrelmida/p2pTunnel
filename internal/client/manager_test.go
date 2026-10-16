package client_test

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"p2pTunnel/internal/client"
	"p2pTunnel/internal/crypto"
)

// A relay that completes TLS but never answers the room handshake must not
// hold JoinRoom past its context (the GUI's Cancel button relies on this).
func TestJoinRoomHonorsContextDuringHandshake(t *testing.T) {
	srvID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvID.SignalingServerTLSConfig())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.(*tls.Conn).Handshake()
				<-done // stay silent
			}()
		}
	}()

	mgr, err := client.NewManager(client.ManagerConfig{ServerAddr: ln.Addr().String(), PeerName: "Tester"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = mgr.JoinRoom(ctx, ln.Addr().String(), "Tester", "ABC123", "")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("JoinRoom error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("JoinRoom took %v after its context expired", elapsed)
	}
	if mgr.Snapshot().Connected {
		t.Fatal("manager reports connected after a failed join")
	}
}
