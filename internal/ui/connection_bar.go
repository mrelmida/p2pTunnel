package ui

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

const (
	prefRelayIP     = "relay_ip"
	prefRelayPort   = "relay_port"
	prefRelaySecret = "relay_secret"
	prefPeerName    = "peer_name"
)

type connectionBar struct {
	ui *UI

	relayIPEntry     *widget.Entry
	relayPortEntry   *widget.Entry
	relaySecretEntry *widget.Entry
	nameEntry        *widget.Entry
	roomCodeEntry    *widget.Entry
	passwordEntry    *widget.Entry

	createBtn *widget.Button
	joinBtn   *widget.Button
	leaveBtn  *widget.Button

	statusLabel *widget.Label
	roomBadge   *widget.Label
	ipBadge     *widget.Label
	copyCodeBtn *widget.Button
	copyIPBtn   *widget.Button

	activeRoomCode string
	activeSelfIP   string

	root fyne.CanvasObject
}

func newConnectionBar(u *UI, defaultIP, defaultPort, defaultSecret, defaultName string) *connectionBar {
	cb := &connectionBar{ui: u}
	prefs := u.app.Preferences()

	savedIP := prefs.StringWithFallback(prefRelayIP, defaultIP)
	savedPort := prefs.StringWithFallback(prefRelayPort, defaultPort)
	savedSecret := prefs.StringWithFallback(prefRelaySecret, defaultSecret)
	savedName := prefs.StringWithFallback(prefPeerName, defaultName)

	cb.relayIPEntry = widget.NewEntry()
	cb.relayIPEntry.SetPlaceHolder("EU Relay IP / Host (e.g. 203.0.113.10)")
	cb.relayIPEntry.SetText(savedIP)

	cb.relayPortEntry = widget.NewEntry()
	cb.relayPortEntry.SetPlaceHolder("4433")
	cb.relayPortEntry.SetText(savedPort)

	cb.relaySecretEntry = widget.NewPasswordEntry()
	cb.relaySecretEntry.SetPlaceHolder("Relay Server Secret")
	cb.relaySecretEntry.SetText(savedSecret)

	cb.nameEntry = widget.NewEntry()
	cb.nameEntry.SetPlaceHolder("Nickname")
	cb.nameEntry.SetText(savedName)

	cb.roomCodeEntry = widget.NewEntry()
	cb.roomCodeEntry.SetPlaceHolder("Code (e.g. K7X-92M or blank for random)")

	cb.passwordEntry = widget.NewPasswordEntry()
	cb.passwordEntry.SetPlaceHolder("Optional room password")

	cb.createBtn = widget.NewButtonWithIcon("Create Room", theme.ContentAddIcon(), cb.onCreateRoom)
	cb.createBtn.Importance = widget.HighImportance

	cb.joinBtn = widget.NewButtonWithIcon("Join Room", theme.LoginIcon(), cb.onJoinRoom)
	cb.joinBtn.Importance = widget.HighImportance

	cb.leaveBtn = widget.NewButtonWithIcon("Disconnect", theme.LogoutIcon(), func() {
		go u.mgr.LeaveRoom()
	})
	cb.leaveBtn.Importance = widget.DangerImportance
	cb.leaveBtn.Disable()

	cb.statusLabel = widget.NewLabelWithStyle("Not connected — Configure your EU Relay Server & Create/Join a Room",
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	cb.roomBadge = widget.NewLabelWithStyle("Code: —", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true})
	cb.ipBadge = widget.NewLabelWithStyle("Your IP: —", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true})

	cb.copyCodeBtn = widget.NewButtonWithIcon("Copy Code", theme.ContentCopyIcon(), func() {
		if cb.activeRoomCode != "" {
			u.win.Clipboard().SetContent(cb.activeRoomCode)
			u.setFooterStatus(fmt.Sprintf("Copied room code %s to clipboard", cb.activeRoomCode))
		}
	})
	cb.copyCodeBtn.Disable()

	cb.copyIPBtn = widget.NewButtonWithIcon("Copy My IP", theme.ContentCopyIcon(), func() {
		if cb.activeSelfIP != "" {
			u.win.Clipboard().SetContent(cb.activeSelfIP)
			u.setFooterStatus(fmt.Sprintf("Copied your room IP %s to clipboard", cb.activeSelfIP))
		}
	})
	cb.copyIPBtn.Disable()

	relayRow := container.NewGridWithColumns(3,
		container.NewBorder(nil, nil, widget.NewLabel("Relay IP:"), nil, cb.relayIPEntry),
		container.NewBorder(nil, nil, widget.NewLabel("Relay Port:"), nil, cb.relayPortEntry),
		container.NewBorder(nil, nil, widget.NewLabel("Relay Secret:"), nil, cb.relaySecretEntry),
	)

	roomRow := container.NewGridWithColumns(3,
		container.NewBorder(nil, nil, widget.NewLabel("Nickname:"), nil, cb.nameEntry),
		container.NewBorder(nil, nil, widget.NewLabel("Room Code:"), nil, cb.roomCodeEntry),
		container.NewBorder(nil, nil, widget.NewLabel("Room Pass:"), nil, cb.passwordEntry),
	)

	actionsRow := container.NewBorder(
		nil, nil,
		container.NewHBox(cb.createBtn, cb.joinBtn, cb.leaveBtn),
		container.NewHBox(cb.roomBadge, cb.copyCodeBtn, widget.NewSeparator(), cb.ipBadge, cb.copyIPBtn),
		cb.statusLabel,
	)

	cb.root = widget.NewCard("", "", container.NewVBox(relayRow, roomRow, actionsRow))
	return cb
}

func (cb *connectionBar) buildServerAddrAndSavePrefs() (string, string, error) {
	ip := strings.TrimSpace(cb.relayIPEntry.Text)
	port := strings.TrimSpace(cb.relayPortEntry.Text)
	secret := cb.relaySecretEntry.Text
	name := strings.TrimSpace(cb.nameEntry.Text)

	if ip == "" {
		return "", "", fmt.Errorf("relay IP / host is required")
	}
	if port == "" {
		port = "4433"
		cb.relayPortEntry.SetText(port)
	}

	prefs := cb.ui.app.Preferences()
	prefs.SetString(prefRelayIP, ip)
	prefs.SetString(prefRelayPort, port)
	prefs.SetString(prefRelaySecret, secret)
	if name != "" {
		prefs.SetString(prefPeerName, name)
	}

	cb.ui.mgr.SetServerSecret(secret)
	return net.JoinHostPort(ip, port), name, nil
}

func (cb *connectionBar) onCreateRoom() {
	srvAddr, name, err := cb.buildServerAddrAndSavePrefs()
	if err != nil {
		dialog.ShowError(err, cb.ui.win)
		return
	}
	customCode := strings.TrimSpace(cb.roomCodeEntry.Text)
	pass := cb.passwordEntry.Text

	cb.setBusy(true, fmt.Sprintf("Creating room on %s...", srvAddr))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		err := cb.ui.mgr.CreateRoom(ctx, srvAddr, name, customCode, pass)
		fyne.Do(func() {
			cb.setBusy(false, "")
			if err != nil {
				dialog.ShowError(err, cb.ui.win)
			}
		})
	}()
}

func (cb *connectionBar) onJoinRoom() {
	srvAddr, name, err := cb.buildServerAddrAndSavePrefs()
	if err != nil {
		dialog.ShowError(err, cb.ui.win)
		return
	}
	code := strings.TrimSpace(cb.roomCodeEntry.Text)
	pass := cb.passwordEntry.Text

	if code == "" {
		dialog.ShowError(fmt.Errorf("please enter the shared room code to join"), cb.ui.win)
		return
	}

	cb.setBusy(true, fmt.Sprintf("Joining room %s on %s...", strings.ToUpper(code), srvAddr))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		err := cb.ui.mgr.JoinRoom(ctx, srvAddr, name, code, pass)
		fyne.Do(func() {
			cb.setBusy(false, "")
			if err != nil {
				dialog.ShowError(err, cb.ui.win)
			}
		})
	}()
}

func (cb *connectionBar) setBusy(busy bool, msg string) {
	if busy {
		cb.createBtn.Disable()
		cb.joinBtn.Disable()
		if msg != "" {
			cb.statusLabel.SetText(msg)
		}
		return
	}
	cb.refresh(cb.ui.mgr.Snapshot())
}

func (cb *connectionBar) refresh(snap client.Snapshot) {
	cb.activeRoomCode = snap.RoomCode
	cb.activeSelfIP = snap.SelfPeer.VirtualIP

	if snap.Connected {
		cb.relayIPEntry.Disable()
		cb.relayPortEntry.Disable()
		cb.relaySecretEntry.Disable()
		cb.nameEntry.Disable()
		cb.roomCodeEntry.SetText(snap.RoomCode)
		cb.roomCodeEntry.Disable()
		cb.passwordEntry.Disable()

		cb.createBtn.Disable()
		cb.joinBtn.Disable()
		cb.leaveBtn.Enable()

		cb.roomBadge.SetText(fmt.Sprintf("Code: %s", snap.RoomCode))
		cb.ipBadge.SetText(fmt.Sprintf("Your IP: %s", snap.SelfPeer.VirtualIP))
		cb.copyCodeBtn.Enable()
		cb.copyIPBtn.Enable()

		peerCount := len(snap.Peers) + 1
		cb.statusLabel.SetText(fmt.Sprintf("Connected (%d peer(s) in room) — Others reach your shared ports at %s",
			peerCount, snap.SelfPeer.VirtualIP))
	} else {
		cb.relayIPEntry.Enable()
		cb.relayPortEntry.Enable()
		cb.relaySecretEntry.Enable()
		cb.nameEntry.Enable()
		cb.roomCodeEntry.Enable()
		cb.passwordEntry.Enable()

		cb.createBtn.Enable()
		cb.joinBtn.Enable()
		cb.leaveBtn.Disable()

		cb.roomBadge.SetText("Code: —")
		cb.ipBadge.SetText("Your IP: —")
		cb.copyCodeBtn.Disable()
		cb.copyIPBtn.Disable()

		cb.statusLabel.SetText("Disconnected — Configure Relay IP, Port & Secret and Create or Join a room")
	}
}
