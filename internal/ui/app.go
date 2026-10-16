package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

// UI holds the Fyne desktop application state and views.
//
// The window shows one of two screens: the connect screen (pick a name and
// room) or the room screen (people, ports). Relay settings live in a dialog
// and the activity log in its own window, so neither crowds the main view.
type UI struct {
	app fyne.App
	win fyne.Window
	mgr *client.Manager

	connectView *connectView
	roomView    *roomView
	logWindow   *logWindow

	statusText *widget.RichText
	flashSeq   int
	flashing   bool
	idleStatus string

	viewInit     bool
	connected    bool
	lastRoomCode string
	userLeft     bool // set by "Leave room" so a deliberate exit isn't reported as a drop
}

// Run launches the Fyne desktop GUI application.
func Run(defaultIP, defaultPort, defaultSecret, defaultName string, forceRelay bool) error {
	fyneApp := app.NewWithID("io.p2ptunnel.desktop")
	fyneApp.Settings().SetTheme(&tunnelTheme{})
	fyneApp.SetIcon(logoResource)
	win := fyneApp.NewWindow("p2pTunnel")
	win.SetMaster()
	win.Resize(fyne.NewSize(960, 640))

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

	u.connectView = newConnectView(u, defaultIP, defaultPort, defaultSecret, defaultName)
	u.roomView = newRoomView(u)
	u.logWindow = newLogWindow(u)

	u.statusText = newMutedText(theme.SizeNameCaptionText)
	logBtn := widget.NewButtonWithIcon("Activity log", theme.ListIcon(), u.logWindow.show)
	logBtn.Importance = widget.LowImportance
	statusBar := container.NewStack(
		canvas.NewRectangle(theme.Color(colorNameShaft)),
		container.NewBorder(widget.NewSeparator(), nil, nil, logBtn,
			container.NewPadded(u.statusText)),
	)

	win.SetContent(container.NewBorder(nil, statusBar, nil, nil,
		container.NewStack(u.connectView.root, u.roomView.root)))
	u.refreshAll(mgr.Snapshot())

	// Periodic refresh for live traffic counters and ping updates.
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

func (u *UI) refreshAll(snap client.Snapshot) {
	if u.connectView == nil || u.roomView == nil || u.logWindow == nil {
		return
	}

	if !u.viewInit || snap.Connected != u.connected {
		wasConnected := u.viewInit && u.connected
		u.viewInit = true
		u.connected = snap.Connected
		if snap.Connected {
			u.lastRoomCode = snap.RoomCode
			u.userLeft = false
			u.connectView.root.Hide()
			u.roomView.root.Show()
		} else {
			u.roomView.root.Hide()
			u.connectView.root.Show()
			if wasConnected {
				// Prefill the code so rejoining is one click, and explain
				// an exit the user didn't ask for.
				u.connectView.codeEntry.SetText(u.lastRoomCode)
				if !u.userLeft {
					u.connectView.setStatus(fmt.Sprintf(
						"Lost connection to room %s. Join again to reconnect.", u.lastRoomCode),
						widget.DangerImportance)
				}
			}
		}
	}

	if snap.Connected {
		u.roomView.refresh(snap)
		u.idleStatus = fmt.Sprintf("Connected · relay server %s", snap.ServerAddr)
	} else {
		u.idleStatus = "Not connected"
	}
	u.logWindow.refresh(snap)

	if !u.flashing {
		setMutedText(u.statusText, u.idleStatus, "")
	}
}

// flash shows a short-lived message in the status bar, e.g. after copying.
func (u *UI) flash(msg string) {
	u.flashSeq++
	seq := u.flashSeq
	u.flashing = true
	setMutedText(u.statusText, msg, theme.ColorNameForeground)
	time.AfterFunc(4*time.Second, func() {
		fyne.Do(func() {
			if u.flashSeq == seq {
				u.flashing = false
				setMutedText(u.statusText, u.idleStatus, "")
			}
		})
	})
}

// copyToClipboard copies value and confirms it in the status bar.
func (u *UI) copyToClipboard(value, what string) {
	if value == "" {
		return
	}
	u.app.Clipboard().SetContent(value)
	u.flash(fmt.Sprintf("Copied %s %s to the clipboard", what, value))
}
