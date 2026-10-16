package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

// UI holds the Fyne desktop application state and panels.
type UI struct {
	app fyne.App
	win fyne.Window
	mgr *client.Manager

	connBar    *connectionBar
	peersPanel *peersPanel
	portsPanel *portsPanel

	logEntry   *widget.Entry
	footerText *widget.Label
	lastLogLen int
}

// Run launches the Fyne desktop GUI application.
func Run(defaultIP, defaultPort, defaultSecret, defaultName string, forceRelay bool) error {
	fyneApp := app.NewWithID("io.p2ptunnel.desktop")
	win := fyneApp.NewWindow("p2pTunnel — P2P Port & IP Sharing")
	win.Resize(fyne.NewSize(1040, 740))

	u := &UI{
		app: fyneApp,
		win: win,
	}

	if defaultIP == "" {
		defaultIP = "127.0.0.1"
	}
	if defaultPort == "" {
		defaultPort = "4433"
	}

	mgr, err := client.NewManager(client.ManagerConfig{
		ServerAddr:   fmt.Sprintf("%s:%s", defaultIP, defaultPort),
		ServerSecret: defaultSecret,
		PeerName:     defaultName,
		ForceRelay:   forceRelay,
		OnStateChange: func(snap client.Snapshot) {
			fyne.Do(func() {
				u.refreshAll(snap)
			})
		},
	})
	if err != nil {
		return fmt.Errorf("initialize client manager: %w", err)
	}
	u.mgr = mgr

	u.connBar = newConnectionBar(u, defaultIP, defaultPort, defaultSecret, defaultName)
	u.peersPanel = newPeersPanel(u)
	u.portsPanel = newPortsPanel(u)

	u.logEntry = widget.NewMultiLineEntry()
	u.logEntry.Wrapping = fyne.TextWrapWord
	u.logEntry.TextStyle = fyne.TextStyle{Monospace: true}

	u.footerText = widget.NewLabel("Ready — Zero-admin loopback IP mode (127.0.0.x)")

	copyLogBtn := widget.NewButtonWithIcon("Copy Log", theme.ContentCopyIcon(), func() {
		win.Clipboard().SetContent(u.logEntry.Text)
		u.setFooterStatus("Copied activity log to clipboard")
	})

	logAccordion := widget.NewAccordion(
		widget.NewAccordionItem("Activity & Connection Log", container.NewBorder(
			nil,
			container.NewBorder(nil, nil, nil, copyLogBtn, u.footerText),
			nil, nil,
			container.NewGridWrap(fyne.NewSize(980, 115), u.logEntry),
		)),
	)
	logAccordion.Open(0)

	mainSplit := container.NewHSplit(u.peersPanel.root, u.portsPanel.root)
	mainSplit.Offset = 0.34

	content := container.NewBorder(
		u.connBar.root,
		logAccordion,
		nil, nil,
		mainSplit,
	)

	win.SetContent(content)
	u.refreshAll(mgr.Snapshot())

	// Periodic 1.5s refresh for live traffic counters and ping updates.
	stopTicker := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopTicker:
				return
			case <-ticker.C:
				snap := mgr.Snapshot()
				fyne.Do(func() {
					u.refreshAll(snap)
				})
			}
		}
	}()

	win.SetOnClosed(func() {
		close(stopTicker)
		_ = mgr.Close()
	})

	win.ShowAndRun()
	return nil
}

func (u *UI) setFooterStatus(msg string) {
	if u.footerText != nil {
		u.footerText.SetText(msg)
	}
}

func (u *UI) refreshAll(snap client.Snapshot) {
	if u.connBar == nil || u.peersPanel == nil || u.portsPanel == nil {
		return
	}
	u.connBar.refresh(snap)
	u.peersPanel.refresh(snap)
	u.portsPanel.refresh(snap)

	if len(snap.Logs) != u.lastLogLen {
		u.lastLogLen = len(snap.Logs)
		u.logEntry.SetText(strings.Join(snap.Logs, "\n"))
		u.logEntry.CursorRow = len(snap.Logs)
	}
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
