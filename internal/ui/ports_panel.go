package ui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

type portsPanel struct {
	ui *UI

	protoSelect *widget.Select
	portEntry   *widget.Entry
	labelEntry  *widget.Entry

	mu             sync.RWMutex
	localShares    []client.LocalShareStatus
	remoteBindings []client.RemoteBindingStatus

	localList   *widget.List
	localEmpty  *widget.Label
	remoteList  *widget.List
	remoteEmpty *widget.Label

	root fyne.CanvasObject
}

func newPortsPanel(u *UI) *portsPanel {
	pp := &portsPanel{ui: u}

	// --- Top section: Share a Local Port ---
	pp.protoSelect = widget.NewSelect([]string{"TCP", "UDP", "TCP + UDP"}, nil)
	pp.protoSelect.SetSelected("TCP")

	pp.portEntry = widget.NewEntry()
	pp.portEntry.SetPlaceHolder("Port (e.g. 25565)")

	pp.labelEntry = widget.NewEntry()
	pp.labelEntry.SetPlaceHolder("Label (e.g. Minecraft, Web Dev)")

	shareBtn := widget.NewButtonWithIcon("Share Port", theme.ContentAddIcon(), pp.onSharePort)
	shareBtn.Importance = widget.HighImportance

	presetMC := widget.NewButton("Minecraft (:25565)", func() {
		pp.protoSelect.SetSelected("TCP + UDP")
		pp.portEntry.SetText("25565")
		pp.labelEntry.SetText("Minecraft")
		pp.onSharePort()
	})
	presetWeb := widget.NewButton("HTTP (:8080)", func() {
		pp.protoSelect.SetSelected("TCP")
		pp.portEntry.SetText("8080")
		pp.labelEntry.SetText("Web Server")
		pp.onSharePort()
	})
	presetVite := widget.NewButton("Dev (:5173)", func() {
		pp.protoSelect.SetSelected("TCP")
		pp.portEntry.SetText("5173")
		pp.labelEntry.SetText("Dev Server")
		pp.onSharePort()
	})

	shareForm := container.NewVBox(
		container.NewBorder(
			nil, nil,
			pp.protoSelect,
			shareBtn,
			container.NewGridWithColumns(2, pp.portEntry, pp.labelEntry),
		),
		container.NewHBox(
			widget.NewLabel("Quick Share:"),
			presetMC,
			presetWeb,
			presetVite,
		),
	)

	pp.localEmpty = widget.NewLabelWithStyle(
		"No local ports shared yet. Enter a port above so connected peers can reach your service.",
		fyne.TextAlignCenter,
		fyne.TextStyle{Italic: true},
	)
	pp.localEmpty.Wrapping = fyne.TextWrapWord

	pp.localList = widget.NewList(
		func() int {
			pp.mu.RLock()
			defer pp.mu.RUnlock()
			return len(pp.localShares)
		},
		func() fyne.CanvasObject {
			titleLbl := widget.NewLabelWithStyle("TCP :25565 — Minecraft", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			statsLbl := widget.NewLabel("0 active conns • 0 B transferred")
			stopBtn := widget.NewButtonWithIcon("Stop", theme.DeleteIcon(), nil)
			stopBtn.Importance = widget.DangerImportance
			return container.NewBorder(nil, nil, nil, stopBtn, container.NewVBox(titleLbl, statsLbl))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			pp.mu.RLock()
			if id < 0 || id >= len(pp.localShares) {
				pp.mu.RUnlock()
				return
			}
			sh := pp.localShares[id]
			pp.mu.RUnlock()

			border := obj.(*fyne.Container)
			left := border.Objects[0].(*fyne.Container)
			stopBtn := border.Objects[1].(*widget.Button)

			titleLbl := left.Objects[0].(*widget.Label)
			statsLbl := left.Objects[1].(*widget.Label)

			labelPart := sh.Name
			if labelPart == "" {
				labelPart = "Shared Service"
			}
			titleLbl.SetText(fmt.Sprintf("%s :%d — %s", strings.ToUpper(sh.Protocol), sh.Port, labelPart))
			statsLbl.SetText(fmt.Sprintf("%d active connection(s) • %s transferred", sh.ActiveConns, formatBytes(sh.BytesTotal)))

			proto := sh.Protocol
			port := sh.Port
			stopBtn.OnTapped = func() {
				pp.ui.mgr.RemoveSharedPort(proto, port)
			}
		},
	)

	localCard := widget.NewCard(
		"My Shared Ports (Exposed to Room)",
		"Only ports listed here can be accessed by room members",
		container.NewBorder(
			container.NewVBox(shareForm, widget.NewSeparator()),
			nil, nil, nil,
			container.NewStack(pp.localList, pp.localEmpty),
		),
	)

	// --- Bottom section: Remote Ports from Room Peers ---
	pp.remoteEmpty = widget.NewLabelWithStyle(
		"No remote ports shared by peers yet. When someone in the room shares a port, it will automatically bind here on their 127.0.0.x IP.",
		fyne.TextAlignCenter,
		fyne.TextStyle{Italic: true},
	)
	pp.remoteEmpty.Wrapping = fyne.TextWrapWord

	pp.remoteList = widget.NewList(
		func() int {
			pp.mu.RLock()
			defer pp.mu.RUnlock()
			return len(pp.remoteBindings)
		},
		func() fyne.CanvasObject {
			titleLbl := widget.NewLabelWithStyle("Bob — Minecraft (TCP :25565)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			addrLbl := widget.NewLabelWithStyle("Connect at: 127.0.0.3:25565", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
			copyBtn := widget.NewButtonWithIcon("Copy Address", theme.ContentCopyIcon(), nil)
			copyBtn.Importance = widget.HighImportance
			customBtn := widget.NewButtonWithIcon("Custom Port...", theme.SettingsIcon(), nil)
			toggleBtn := widget.NewButton("Pause", nil)

			btns := container.NewHBox(copyBtn, customBtn, toggleBtn)
			return container.NewBorder(nil, nil, nil, btns, container.NewVBox(titleLbl, addrLbl))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			pp.mu.RLock()
			if id < 0 || id >= len(pp.remoteBindings) {
				pp.mu.RUnlock()
				return
			}
			rb := pp.remoteBindings[id]
			pp.mu.RUnlock()

			border := obj.(*fyne.Container)
			left := border.Objects[0].(*fyne.Container)
			btns := border.Objects[1].(*fyne.Container)

			titleLbl := left.Objects[0].(*widget.Label)
			addrLbl := left.Objects[1].(*widget.Label)

			copyBtn := btns.Objects[0].(*widget.Button)
			customBtn := btns.Objects[1].(*widget.Button)
			toggleBtn := btns.Objects[2].(*widget.Button)

			svcName := rb.Name
			if svcName == "" {
				svcName = "Port"
			}
			titleLbl.SetText(fmt.Sprintf("%s — %s (%s :%d)", rb.PeerName, svcName, strings.ToUpper(rb.Protocol), rb.RemotePort))

			displayAddr := rb.ActualBound
			if displayAddr == "" {
				displayAddr = fmt.Sprintf("%s:%d", rb.BindIP, rb.LocalPort)
			}

			if !rb.Enabled {
				addrLbl.SetText(fmt.Sprintf("PAUSED (would bind %s)", displayAddr))
				toggleBtn.SetText("Enable")
				copyBtn.Disable()
			} else if rb.StatusErr != "" {
				addrLbl.SetText(fmt.Sprintf("Bind Error: %s", rb.StatusErr))
				toggleBtn.SetText("Pause")
				copyBtn.Disable()
			} else {
				addrLbl.SetText(fmt.Sprintf("Ready at %s  •  %d active  •  %s",
					displayAddr, rb.ActiveConns, formatBytes(rb.BytesTotal)))
				toggleBtn.SetText("Pause")
				copyBtn.Enable()
			}

			addrToCopy := displayAddr
			copyBtn.OnTapped = func() {
				pp.ui.win.Clipboard().SetContent(addrToCopy)
				pp.ui.setFooterStatus(fmt.Sprintf("Copied %s to clipboard", addrToCopy))
			}

			bindingCopy := rb
			customBtn.OnTapped = func() {
				pp.showCustomBindDialog(bindingCopy)
			}
			toggleBtn.OnTapped = func() {
				pp.ui.mgr.SetRemotePortEnabled(bindingCopy.Key, !bindingCopy.Enabled)
			}
		},
	)

	remoteCard := widget.NewCard(
		"Available Remote Ports (Auto-Mapped from Peers)",
		"Use Copy Address to paste the 127.0.0.x:PORT into your game, browser, or client",
		container.NewStack(pp.remoteList, pp.remoteEmpty),
	)

	split := container.NewVSplit(localCard, remoteCard)
	split.Offset = 0.46
	pp.root = split
	return pp
}

func (pp *portsPanel) onSharePort() {
	portStr := strings.TrimSpace(pp.portEntry.Text)
	val, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || val == 0 {
		dialog.ShowError(fmt.Errorf("please enter a valid port number between 1 and 65535"), pp.ui.win)
		return
	}

	protoChoice := pp.protoSelect.Selected
	proto := "tcp"
	switch protoChoice {
	case "UDP":
		proto = "udp"
	case "TCP + UDP":
		proto = "both"
	}

	name := strings.TrimSpace(pp.labelEntry.Text)
	if err := pp.ui.mgr.AddSharedPort(proto, uint16(val), name); err != nil {
		dialog.ShowError(err, pp.ui.win)
		return
	}
	pp.portEntry.SetText("")
	pp.labelEntry.SetText("")
}

func (pp *portsPanel) showCustomBindDialog(rb client.RemoteBindingStatus) {
	ipEntry := widget.NewEntry()
	ipEntry.SetText(rb.BindIP)
	ipEntry.SetPlaceHolder(rb.PeerIP)

	portEntry := widget.NewEntry()
	portEntry.SetText(strconv.Itoa(int(rb.LocalPort)))
	portEntry.SetPlaceHolder(strconv.Itoa(int(rb.RemotePort)))

	resetBtn := widget.NewButton("Reset to Peer Default ("+rb.PeerIP+")", func() {
		ipEntry.SetText(rb.PeerIP)
		portEntry.SetText(strconv.Itoa(int(rb.RemotePort)))
	})

	items := []*widget.FormItem{
		widget.NewFormItem("Local Bind IP", ipEntry),
		widget.NewFormItem("Local Port", portEntry),
		widget.NewFormItem("", resetBtn),
	}

	dialog.ShowForm(
		fmt.Sprintf("Override Local Mapping (%s %s:%d)", rb.PeerName, strings.ToUpper(rb.Protocol), rb.RemotePort),
		"Apply",
		"Cancel",
		items,
		func(ok bool) {
			if !ok {
				return
			}
			pVal, err := strconv.ParseUint(strings.TrimSpace(portEntry.Text), 10, 16)
			if err != nil || pVal == 0 {
				dialog.ShowError(fmt.Errorf("invalid port number"), pp.ui.win)
				return
			}
			if err := pp.ui.mgr.SetRemotePortOverride(rb.Key, strings.TrimSpace(ipEntry.Text), uint16(pVal)); err != nil {
				dialog.ShowError(err, pp.ui.win)
			}
		},
		pp.ui.win,
	)
}

func (pp *portsPanel) refresh(snap client.Snapshot) {
	pp.mu.Lock()
	pp.localShares = snap.LocalShares
	pp.remoteBindings = snap.RemoteBindings
	pp.mu.Unlock()

	if len(snap.LocalShares) == 0 {
		pp.localEmpty.Show()
		pp.localList.Hide()
	} else {
		pp.localEmpty.Hide()
		pp.localList.Show()
	}
	pp.localList.Refresh()

	if len(snap.RemoteBindings) == 0 {
		pp.remoteEmpty.Show()
		pp.remoteList.Hide()
	} else {
		pp.remoteEmpty.Hide()
		pp.remoteList.Show()
	}
	pp.remoteList.Refresh()
}
