package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

// ---------------------------------------------------------------------------
// Shared with you: other peers' ports, mapped onto this machine.
// ---------------------------------------------------------------------------

type remotePortsPanel struct {
	ui *UI

	items []client.RemoteBindingStatus
	list  *widget.List
	panel fyne.CanvasObject
	empty fyne.CanvasObject
	root  fyne.CanvasObject
}

func newRemotePortsPanel(u *UI) *remotePortsPanel {
	rp := &remotePortsPanel{ui: u}

	rp.empty = newEmptyState(theme.DownloadIcon(), "Nothing shared with you yet",
		"When someone in the room shares a port, it appears here with an address "+
			"you can paste into your game, browser, or app.")

	rp.list = widget.NewList(
		func() int { return len(rp.items) },
		func() fyne.CanvasObject { return newRemoteRow(rp) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(rp.items) {
				return
			}
			obj.(*remoteRow).set(rp.items[id])
		},
	)
	rp.list.OnSelected = func(widget.ListItemID) { rp.list.UnselectAll() }

	rp.panel = newSurface(container.New(layout.NewCustomPaddedLayout(4, 4, 4, 4), rp.list), colorNameSurface)
	rp.root = container.New(layout.NewCustomPaddedLayout(10, 0, 0, 0), container.NewStack(rp.panel, rp.empty))
	return rp
}

func (rp *remotePortsPanel) refresh(snap client.Snapshot) {
	rp.items = snap.RemoteBindings
	if len(rp.items) == 0 {
		rp.panel.Hide()
		rp.empty.Show()
	} else {
		rp.empty.Hide()
		rp.panel.Show()
	}
	rp.list.Refresh()
}

func (rp *remotePortsPanel) showMenu(rb client.RemoteBindingStatus, anchor fyne.CanvasObject) {
	toggle := fyne.NewMenuItem("Pause", func() {
		go rp.ui.mgr.SetRemotePortEnabled(rb.Key, false)
	})
	toggle.Icon = theme.MediaPauseIcon()
	if !rb.Enabled {
		toggle = fyne.NewMenuItem("Resume", func() {
			go rp.ui.mgr.SetRemotePortEnabled(rb.Key, true)
		})
		toggle.Icon = theme.MediaPlayIcon()
	}
	change := fyne.NewMenuItem("Change local address…", func() {
		rp.showCustomBindDialog(rb)
	})
	change.Icon = theme.SettingsIcon()

	widget.ShowPopUpMenuAtRelativePosition(
		fyne.NewMenu("", toggle, change),
		rp.ui.win.Canvas(),
		fyne.NewPos(0, anchor.Size().Height),
		anchor,
	)
}

func (rp *remotePortsPanel) showCustomBindDialog(rb client.RemoteBindingStatus) {
	ipEntry := widget.NewEntry()
	ipEntry.SetText(rb.BindIP)
	ipEntry.SetPlaceHolder(rb.PeerIP)

	portEntry := widget.NewEntry()
	portEntry.SetText(strconv.Itoa(int(rb.LocalPort)))
	portEntry.SetPlaceHolder(strconv.Itoa(int(rb.RemotePort)))
	portEntry.Validator = validatePort

	resetBtn := widget.NewButtonWithIcon("Reset to default", theme.ContentUndoIcon(), func() {
		ipEntry.SetText(rb.PeerIP)
		portEntry.SetText(strconv.Itoa(int(rb.RemotePort)))
	})
	resetBtn.Importance = widget.LowImportance

	d := dialog.NewForm(
		fmt.Sprintf("Local address for %s", remoteTitle(rb)),
		"Apply", "Cancel",
		[]*widget.FormItem{
			{Text: "IP address", Widget: ipEntry, HintText: "Usually the peer's own 127.0.0.x address"},
			{Text: "Port", Widget: portEntry, HintText: "Change this if the port is already in use here"},
			{Text: "", Widget: container.NewHBox(resetBtn)},
		},
		func(ok bool) {
			if !ok {
				return
			}
			pVal, err := strconv.ParseUint(strings.TrimSpace(portEntry.Text), 10, 16)
			if err != nil || pVal == 0 {
				dialog.ShowError(fmt.Errorf("invalid port number"), rp.ui.win)
				return
			}
			bindIP := strings.TrimSpace(ipEntry.Text)
			go func() {
				if err := rp.ui.mgr.SetRemotePortOverride(rb.Key, bindIP, uint16(pVal)); err != nil {
					fyne.Do(func() { dialog.ShowError(err, rp.ui.win) })
				}
			}()
		},
		rp.ui.win,
	)
	d.Resize(fyne.NewSize(460, d.MinSize().Height))
	d.Show()
}

// remoteTitle names a remote port for display, e.g. "Bob's Minecraft (TCP)".
func remoteTitle(rb client.RemoteBindingStatus) string {
	svc := rb.Name
	if svc == "" {
		svc = fmt.Sprintf("port %d", rb.RemotePort)
	}
	return fmt.Sprintf("%s's %s (%s)", rb.PeerName, svc, strings.ToUpper(rb.Protocol))
}

type remoteRow struct {
	widget.BaseWidget

	panel   *remotePortsPanel
	title   *widget.Label
	detail  *widget.RichText
	chip    *copyChip
	moreBtn *widget.Button

	item client.RemoteBindingStatus
}

func newRemoteRow(rp *remotePortsPanel) *remoteRow {
	r := &remoteRow{
		panel:  rp,
		title:  newTitle(),
		detail: newMutedText(theme.SizeNameCaptionText),
		chip: newCopyChip(theme.SizeNameText, theme.ColorNameForeground, func(v string) {
			rp.ui.copyToClipboard(v, "address")
		}),
	}
	r.moreBtn = newIconButton(theme.MoreVerticalIcon(), nil)
	r.moreBtn.OnTapped = func() { r.panel.showMenu(r.item, r.moreBtn) }
	r.ExtendBaseWidget(r)
	return r
}

func (r *remoteRow) set(rb client.RemoteBindingStatus) {
	r.item = rb
	addr := rb.ActualBound
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", rb.BindIP, rb.LocalPort)
	}

	title := rb.Name
	if title == "" {
		title = fmt.Sprintf("Port %d", rb.RemotePort)
	}
	if r.title.Text != title {
		r.title.SetText(title)
	}

	from := fmt.Sprintf("%s · %s", rb.PeerName, strings.ToUpper(rb.Protocol))
	switch {
	case !rb.Enabled:
		setMutedText(r.detail, from+" · Paused", "")
		r.chip.SetValue(addr, true)
	case rb.StatusErr != "":
		setMutedText(r.detail, fmt.Sprintf("%s · Couldn't open %s: %s", from, addr, rb.StatusErr), theme.ColorNameError)
		r.chip.SetValue(addr, true)
	default:
		setMutedText(r.detail, from+" · "+trafficSummary(rb.ActiveConns, rb.BytesTotal), "")
		r.chip.SetValue(addr, false)
	}
}

func (r *remoteRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.New(layout.NewCustomPaddedLayout(4, 4, 10, 2),
		container.NewBorder(nil, nil, nil,
			container.NewHBox(container.NewCenter(r.chip), container.NewCenter(r.moreBtn)),
			twoLines(r.title, r.detail))))
}

// ---------------------------------------------------------------------------
// Your shared ports: local services exposed to the room.
// ---------------------------------------------------------------------------

type sharePreset struct {
	label string
	proto string
	port  string
	name  string
}

var sharePresets = []sharePreset{
	{"Minecraft (TCP + UDP 25565)", "TCP + UDP", "25565", "Minecraft"},
	{"Web server (TCP 8080)", "TCP", "8080", "Web server"},
	{"Vite dev server (TCP 5173)", "TCP", "5173", "Dev server"},
}

type localSharesPanel struct {
	ui *UI

	protoSelect *widget.Select
	portEntry   *widget.Entry
	nameEntry   *widget.Entry

	items []client.LocalShareStatus
	list  *widget.List
	panel fyne.CanvasObject
	empty fyne.CanvasObject
	root  fyne.CanvasObject
}

func newLocalSharesPanel(u *UI) *localSharesPanel {
	lp := &localSharesPanel{ui: u}

	lp.protoSelect = widget.NewSelect([]string{"TCP", "UDP", "TCP + UDP"}, nil)
	lp.protoSelect.SetSelected("TCP")

	lp.portEntry = widget.NewEntry()
	lp.portEntry.SetPlaceHolder("Port, e.g. 25565")
	lp.portEntry.OnSubmitted = func(string) { lp.onShare() }

	lp.nameEntry = widget.NewEntry()
	lp.nameEntry.SetPlaceHolder("Name (optional)")
	lp.nameEntry.OnSubmitted = func(string) { lp.onShare() }

	shareBtn := widget.NewButtonWithIcon("Share", theme.ContentAddIcon(), lp.onShare)
	shareBtn.Importance = widget.HighImportance

	var presetsBtn *widget.Button
	presetsBtn = widget.NewButtonWithIcon("Presets", theme.MenuDropDownIcon(), func() {
		items := make([]*fyne.MenuItem, 0, len(sharePresets))
		for _, p := range sharePresets {
			items = append(items, fyne.NewMenuItem(p.label, func() {
				lp.protoSelect.SetSelected(p.proto)
				lp.portEntry.SetText(p.port)
				lp.nameEntry.SetText(p.name)
				lp.onShare()
			}))
		}
		widget.ShowPopUpMenuAtRelativePosition(fyne.NewMenu("", items...), u.win.Canvas(),
			fyne.NewPos(0, presetsBtn.Size().Height), presetsBtn)
	})
	presetsBtn.Importance = widget.LowImportance

	form := container.NewBorder(nil, nil,
		lp.protoSelect,
		container.NewHBox(shareBtn, presetsBtn),
		container.NewGridWithColumns(2, lp.portEntry, lp.nameEntry),
	)
	hint := newMutedText(theme.SizeNameCaptionText)
	setMutedText(hint, "Only the ports listed here can be reached by people in the room.", "")

	lp.empty = newEmptyState(theme.UploadIcon(), "You're not sharing anything",
		"Add a port above to let people in the room reach a service running on this computer.")

	lp.list = widget.NewList(
		func() int { return len(lp.items) },
		func() fyne.CanvasObject { return newLocalRow(lp) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(lp.items) {
				return
			}
			obj.(*localRow).set(lp.items[id])
		},
	)
	lp.list.OnSelected = func(widget.ListItemID) { lp.list.UnselectAll() }

	lp.panel = newSurface(container.New(layout.NewCustomPaddedLayout(4, 4, 4, 4), lp.list), colorNameSurface)
	lp.root = container.NewBorder(
		container.New(layout.NewCustomPaddedVBoxLayout(-theme.InnerPadding()/2),
			container.New(layout.NewCustomPaddedLayout(10, 0, 0, 0), form),
			container.New(layout.NewCustomPaddedLayout(0, 4, 4, 0), hint)),
		nil, nil, nil,
		container.NewStack(lp.panel, lp.empty),
	)
	return lp
}

func (lp *localSharesPanel) onShare() {
	portStr := strings.TrimSpace(lp.portEntry.Text)
	val, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || val == 0 {
		dialog.ShowError(fmt.Errorf("enter a port number between 1 and 65535"), lp.ui.win)
		return
	}

	proto := "tcp"
	switch lp.protoSelect.Selected {
	case "UDP":
		proto = "udp"
	case "TCP + UDP":
		proto = "both"
	}

	name := strings.TrimSpace(lp.nameEntry.Text)
	if err := lp.ui.mgr.AddSharedPort(proto, uint16(val), name); err != nil {
		dialog.ShowError(err, lp.ui.win)
		return
	}
	lp.portEntry.SetText("")
	lp.nameEntry.SetText("")
}

func (lp *localSharesPanel) refresh(snap client.Snapshot) {
	lp.items = snap.LocalShares
	if len(lp.items) == 0 {
		lp.panel.Hide()
		lp.empty.Show()
	} else {
		lp.empty.Hide()
		lp.panel.Show()
	}
	lp.list.Refresh()
}

type localRow struct {
	widget.BaseWidget

	panel   *localSharesPanel
	title   *widget.Label
	detail  *widget.RichText
	stopBtn *widget.Button

	proto string
	port  uint16
}

func newLocalRow(lp *localSharesPanel) *localRow {
	r := &localRow{
		panel:  lp,
		title:  newTitle(),
		detail: newMutedText(theme.SizeNameCaptionText),
	}
	r.stopBtn = widget.NewButtonWithIcon("Stop sharing", theme.CancelIcon(), func() {
		go r.panel.ui.mgr.RemoveSharedPort(r.proto, r.port)
	})
	r.stopBtn.Importance = widget.LowImportance
	r.ExtendBaseWidget(r)
	return r
}

func (r *localRow) set(sh client.LocalShareStatus) {
	r.proto = sh.Protocol
	r.port = sh.Port

	title := sh.Name
	if title == "" {
		title = fmt.Sprintf("Port %d", sh.Port)
	}
	if r.title.Text != title {
		r.title.SetText(title)
	}
	setMutedText(r.detail, fmt.Sprintf("%s %d · %s",
		strings.ToUpper(sh.Protocol), sh.Port, trafficSummary(sh.ActiveConns, sh.BytesTotal)), "")
}

func (r *localRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.New(layout.NewCustomPaddedLayout(4, 4, 10, 2),
		container.NewBorder(nil, nil, nil, container.NewCenter(r.stopBtn),
			twoLines(r.title, r.detail))))
}

// trafficSummary renders live connection stats, e.g. "2 connections · 1.2 MB".
func trafficSummary(active int32, total uint64) string {
	if active == 0 && total == 0 {
		return "Idle"
	}
	return fmt.Sprintf("%s · %s", plural(int(active), "connection", "connections"), formatBytes(total))
}

func validatePort(s string) error {
	if v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16); err != nil || v == 0 {
		return fmt.Errorf("must be 1–65535")
	}
	return nil
}
