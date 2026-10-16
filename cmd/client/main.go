package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"p2pTunnel/internal/client"
	"p2pTunnel/internal/ui"
)

func main() {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "Peer"
	}

	relayIP := flag.String("ip", "127.0.0.1", "EU relay server IP or hostname")
	relayPort := flag.String("port", "4433", "EU relay server TCP/UDP port")
	relaySecret := flag.String("secret", "", "EU relay server shared secret")
	serverAddr := flag.String("server", "", "Optional host:port override for EU relay server")
	peerName := flag.String("name", hostname, "Display name in the room")
	headless := flag.Bool("headless", false, "Run in headless CLI mode instead of launching the desktop GUI")
	createRoom := flag.Bool("create", false, "In headless mode: create a new room")
	joinCode := flag.String("join", "", "In headless mode: room code to join (or custom code when used with -create)")
	password := flag.String("pass", "", "Optional room password")
	sharesFlag := flag.String("share", "", "Comma-separated ports to share (e.g. tcp:25565:Minecraft,udp:25565:Minecraft)")
	forceRelay := flag.Bool("force-relay", false, "Force traffic through the EU server UDP relay instead of direct P2P")
	flag.Parse()

	ip := strings.TrimSpace(*relayIP)
	port := strings.TrimSpace(*relayPort)
	if *serverAddr != "" {
		if h, p, err := net.SplitHostPort(strings.TrimSpace(*serverAddr)); err == nil {
			ip = h
			port = p
		}
	}

	if !*headless {
		if err := ui.Run(ip, port, *relaySecret, *peerName, *forceRelay); err != nil {
			log.Fatalf("GUI error: %v", err)
		}
		return
	}

	fullAddr := net.JoinHostPort(ip, port)
	runHeadless(fullAddr, *relaySecret, *peerName, *createRoom, *joinCode, *password, *sharesFlag, *forceRelay)
}

func runHeadless(serverAddr, serverSecret, peerName string, create bool, code, password, sharesRaw string, forceRelay bool) {
	var lastLogCount int
	mgr, err := client.NewManager(client.ManagerConfig{
		ServerAddr:   serverAddr,
		ServerSecret: serverSecret,
		PeerName:     peerName,
		ForceRelay:   forceRelay,
		OnStateChange: func(snap client.Snapshot) {
			if len(snap.Logs) > lastLogCount {
				for _, line := range snap.Logs[lastLogCount:] {
					fmt.Println(line)
				}
				lastLogCount = len(snap.Logs)
			}
		},
	})
	if err != nil {
		log.Fatalf("failed to initialize manager: %v", err)
	}
	defer mgr.Close()

	if sharesRaw != "" {
		for _, item := range strings.Split(sharesRaw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			parts := strings.SplitN(item, ":", 3)
			if len(parts) < 2 {
				log.Fatalf("invalid -share spec %q (expected proto:port[:name])", item)
			}
			portVal, err := strconv.ParseUint(parts[1], 10, 16)
			if err != nil || portVal == 0 {
				log.Fatalf("invalid port in -share spec %q", item)
			}
			label := ""
			if len(parts) == 3 {
				label = parts[2]
			}
			if err := mgr.AddSharedPort(parts[0], uint16(portVal), label); err != nil {
				log.Fatalf("AddSharedPort(%q): %v", item, err)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if create {
		if err := mgr.CreateRoom(ctx, serverAddr, peerName, code, password); err != nil {
			log.Fatalf("CreateRoom failed: %v", err)
		}
	} else if code != "" {
		if err := mgr.JoinRoom(ctx, serverAddr, peerName, code, password); err != nil {
			log.Fatalf("JoinRoom failed: %v", err)
		}
	} else {
		log.Fatalf("headless mode requires either -create or -join <CODE>")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\nShutting down p2pTunnel...")
}
