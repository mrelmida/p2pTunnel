package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"p2pTunnel/internal/server"
)

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	ipFlag := flag.String("ip", envOrDefault("RELAY_IP", "0.0.0.0"), "Relay bind IP address (or env RELAY_IP)")
	portFlag := flag.String("port", envOrDefault("RELAY_PORT", "4433"), "Relay TCP/UDP port (or env RELAY_PORT)")
	secretFlag := flag.String("secret", envOrDefault("RELAY_SECRET", ""), "Shared server secret required for clients to connect (or env RELAY_SECRET)")
	listenFlag := flag.String("listen", envOrDefault("RELAY_LISTEN", ""), "Optional full host:port override (takes precedence over -ip and -port)")
	udpAddr := flag.String("udp", envOrDefault("RELAY_UDP", ""), "Optional UDP reflector & relay listen address override")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	listenAddr := strings.TrimSpace(*listenFlag)
	if listenAddr == "" {
		listenAddr = fmt.Sprintf("%s:%s", strings.TrimSpace(*ipFlag), strings.TrimSpace(*portFlag))
	}

	srv, err := server.New(server.Config{
		ListenAddr:   listenAddr,
		UDPAddr:      *udpAddr,
		ServerSecret: *secretFlag,
		Logger:       logger,
	})
	if err != nil {
		logger.Fatalf("failed to initialize server: %v", err)
	}

	if strings.TrimSpace(*secretFlag) != "" {
		logger.Printf("[server] relay server secret authentication: ENABLED")
	} else {
		logger.Printf("[server] relay server secret authentication: DISABLED (open server)")
	}

	if err := srv.Start(); err != nil {
		logger.Fatalf("failed to start server: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Printf("[server] received signal %v, shutting down...", sig)
	_ = srv.Close()
}
