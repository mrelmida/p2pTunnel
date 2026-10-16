package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	prefRelayIP     = "relay_ip"
	prefRelayPort   = "relay_port"
	prefRelaySecret = "relay_secret"
	prefPeerName    = "peer_name"
)

// connectView is the screen shown while not in a room: name, room code,
// password, and Join/Create. Relay server settings are edited in a dialog.
type connectView struct {
	ui *UI

	nameEntry *widget.Entry
	codeEntry *widget.Entry
	passEntry *widget.Entry

	joinBtn   *widget.Button
	createBtn *widget.Button
	relayBtn  *widget.Button
	cancelBtn *widget.Button

	relayText  *widget.RichText
	activity   *widget.Activity
	statusText *widget.Label
	statusRow  *fyne.Container

	relayHost   string
	relayPort   string
	relaySecret string

	busy   bool
	cancel context.CancelFunc

	root fyne.CanvasObject
}

func newConnectView(u *UI, defaultIP, defaultPort, defaultSecret, defaultName string) *connectView {
	cv := &connectView{ui: u}
	prefs := u.app.Preferences()

	cv.relayHost = prefs.StringWithFallback(prefRelayIP, defaultIP)
	cv.relayPort = prefs.StringWithFallback(prefRelayPort, defaultPort)
	cv.relaySecret = prefs.StringWithFallback(prefRelaySecret, defaultSecret)

	cv.nameEntry = widget.NewEntry()
	cv.nameEntry.SetPlaceHolder("How others see you")
	cv.nameEntry.SetText(prefs.StringWithFallback(prefPeerName, defaultName))

	cv.codeEntry = widget.NewEntry()
	cv.codeEntry.SetPlaceHolder("K7X-92M")
	cv.codeEntry.TextStyle = fyne.TextStyle{Monospace: true}
	cv.codeEntry.OnSubmitted = func(string) { cv.start(false) }

	cv.passEntry = widget.NewPasswordEntry()
	cv.passEntry.SetPlaceHolder("Optional")
	cv.passEntry.OnSubmitted = func(string) { cv.start(false) }

	cv.joinBtn = widget.NewButtonWithIcon("Join room", theme.LoginIcon(), func() { cv.start(false) })
	cv.joinBtn.Importance = widget.HighImportance
	cv.createBtn = widget.NewButtonWithIcon("Create room", theme.ContentAddIcon(), func() { cv.start(true) })

	cv.cancelBtn = widget.NewButton("Cancel", func() {
		if cv.cancel != nil {
			cv.cancel()
		}
	})
	cv.cancelBtn.Importance = widget.LowImportance

	cv.activity = widget.NewActivity()
	cv.activity.Hide()
	cv.cancelBtn.Hide()
	// The status row always keeps its space so the centered card doesn't
	// jump when a message appears.
	cv.statusText = widget.NewLabel("")
	cv.statusText.Wrapping = fyne.TextWrapWord
	cv.statusRow = container.NewBorder(nil, nil, cv.activity, cv.cancelBtn, cv.statusText)

	cv.relayText = newMutedText(theme.SizeNameCaptionText)
	cv.relayBtn = widget.NewButtonWithIcon("Relay settings", theme.SettingsIcon(), cv.showRelaySettings)
	cv.relayBtn.Importance = widget.LowImportance
	cv.updateRelayText()

	logo := canvas.NewImageFromResource(logoResource)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSquareSize(52))

	title := widget.NewLabelWithStyle("p2pTunnel", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	title.SizeName = theme.SizeNameHeadingText
	subtitle := widget.NewRichText(&widget.TextSegment{
		Text: "Encrypted peer-to-peer port sharing with friends.",
		Style: widget.RichTextStyle{
			Alignment: fyne.TextAlignCenter,
			ColorName: theme.ColorNamePlaceHolder,
		},
	})
	subtitle.Wrapping = fyne.TextWrapWord

	codeHint := newMutedText(theme.SizeNameCaptionText)
	setMutedText(codeHint, "Creating a room? Leave this empty for a random code.", "")

	form := container.NewVBox(
		labelled("Your name", cv.nameEntry),
		container.New(layout.NewCustomPaddedVBoxLayout(-theme.InnerPadding()),
			labelled("Room code", cv.codeEntry), codeHint),
		labelled("Password", cv.passEntry),
		container.NewGridWithColumns(2, cv.createBtn, cv.joinBtn),
		cv.statusRow,
	)

	card := newSurface(container.New(layout.NewCustomPaddedLayout(14, 6, 20, 20), form), colorNameSurface)
	cardBg := card.Objects[0].(*canvas.Rectangle)
	cardBg.Shadow = canvas.Shadow{
		Color:      theme.Color(theme.ColorNameShadow),
		BlurRadius: 28,
		Offset:     fyne.NewPos(0, 10),
		Variant:    canvas.DropShadow,
	}

	relayIcon := widget.NewIcon(theme.NewColoredResource(theme.ComputerIcon(), theme.ColorNamePlaceHolder))
	relayRow := container.NewBorder(nil, nil, relayIcon, cv.relayBtn, cv.relayText)

	content := container.NewVBox(
		container.NewCenter(logo),
		title,
		subtitle,
		card,
		container.New(layout.NewCustomPaddedLayout(4, 0, 8, 0), relayRow),
	)

	cv.root = container.New(&maxWidthLayout{width: 440, center: true}, container.NewPadded(content))
	return cv
}

func (cv *connectView) updateRelayText() {
	if strings.TrimSpace(cv.relayHost) == "" {
		setMutedText(cv.relayText, "No relay server configured", theme.ColorNameWarning)
		return
	}
	secret := "no secret"
	if cv.relaySecret != "" {
		secret = "secret set"
	}
	setMutedText(cv.relayText,
		fmt.Sprintf("Relay %s · %s", net.JoinHostPort(cv.relayHost, cv.relayPort), secret), "")
}

func (cv *connectView) setStatus(msg string, importance widget.Importance) {
	cv.statusText.Importance = importance
	cv.statusText.SetText(msg)
}

func (cv *connectView) setBusy(busy bool, msg string) {
	cv.busy = busy
	for _, w := range []fyne.Disableable{
		cv.nameEntry, cv.codeEntry, cv.passEntry, cv.createBtn, cv.joinBtn, cv.relayBtn,
	} {
		if busy {
			w.Disable()
		} else {
			w.Enable()
		}
	}
	if busy {
		cv.activity.Show()
		cv.activity.Start()
		cv.cancelBtn.Show()
	} else {
		cv.activity.Stop()
		cv.activity.Hide()
		cv.cancelBtn.Hide()
	}
	cv.setStatus(msg, widget.MediumImportance)
}

// start creates (create=true) or joins a room in the background.
func (cv *connectView) start(create bool) {
	if cv.busy {
		return
	}
	if strings.TrimSpace(cv.relayHost) == "" {
		cv.showRelaySettings()
		return
	}

	name := strings.TrimSpace(cv.nameEntry.Text)
	code := strings.ToUpper(strings.TrimSpace(cv.codeEntry.Text))
	pass := cv.passEntry.Text
	if !create && code == "" {
		cv.setStatus("Enter the room code you were given, or create a new room.", widget.DangerImportance)
		cv.ui.win.Canvas().Focus(cv.codeEntry)
		return
	}
	if name != "" {
		cv.ui.app.Preferences().SetString(prefPeerName, name)
	}

	srvAddr := net.JoinHostPort(cv.relayHost, cv.relayPort)
	cv.ui.mgr.SetServerSecret(cv.relaySecret)

	msg := fmt.Sprintf("Joining room %s…", code)
	if create {
		msg = "Creating room…"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cv.cancel = cancel
	cv.setBusy(true, msg)

	go func() {
		defer cancel()
		var err error
		if create {
			err = cv.ui.mgr.CreateRoom(ctx, srvAddr, name, code, pass)
		} else {
			err = cv.ui.mgr.JoinRoom(ctx, srvAddr, name, code, pass)
		}
		fyne.Do(func() {
			cv.cancel = nil
			cv.setBusy(false, "")
			switch {
			case err == nil:
			case errors.Is(err, context.Canceled):
				cv.setStatus("Cancelled.", widget.MediumImportance)
			default:
				action := "join the room"
				if create {
					action = "create the room"
				}
				cv.setStatus(fmt.Sprintf("Couldn't %s: %v", action, err), widget.DangerImportance)
			}
		})
	}()
}

func (cv *connectView) showRelaySettings() {
	hostEntry := widget.NewEntry()
	hostEntry.SetPlaceHolder("relay.example.com or 203.0.113.10")
	hostEntry.SetText(cv.relayHost)
	hostEntry.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("required")
		}
		return nil
	}

	portEntry := widget.NewEntry()
	portEntry.SetPlaceHolder("4433")
	portEntry.SetText(cv.relayPort)
	portEntry.Validator = func(s string) error {
		if s = strings.TrimSpace(s); s == "" {
			return nil
		}
		if v, err := strconv.ParseUint(s, 10, 16); err != nil || v == 0 {
			return errors.New("must be 1–65535")
		}
		return nil
	}

	secretEntry := widget.NewPasswordEntry()
	secretEntry.SetText(cv.relaySecret)

	d := dialog.NewForm("Relay server", "Save", "Cancel", []*widget.FormItem{
		{Text: "Host", Widget: hostEntry, HintText: "Address of your p2pTunnel relay server"},
		{Text: "Port", Widget: portEntry, HintText: "TCP and UDP port (default 4433)"},
		{Text: "Secret", Widget: secretEntry, HintText: "The RELAY_SECRET configured on the server"},
	}, func(ok bool) {
		if !ok {
			return
		}
		cv.relayHost = strings.TrimSpace(hostEntry.Text)
		cv.relayPort = strings.TrimSpace(portEntry.Text)
		if cv.relayPort == "" {
			cv.relayPort = "4433"
		}
		cv.relaySecret = secretEntry.Text

		prefs := cv.ui.app.Preferences()
		prefs.SetString(prefRelayIP, cv.relayHost)
		prefs.SetString(prefRelayPort, cv.relayPort)
		prefs.SetString(prefRelaySecret, cv.relaySecret)
		cv.updateRelayText()
		cv.ui.flash("Relay settings saved")
	}, cv.ui.win)
	d.Resize(fyne.NewSize(480, d.MinSize().Height))
	d.Show()
}
