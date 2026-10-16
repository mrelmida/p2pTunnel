package ui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

type peerItem struct {
	name      string
	virtualIP string
	detail    string
	dot       fyne.ThemeColorName
}

// peersPanel lists everyone in the room (you first) with a link LED showing
// how the tunnel to them is routed, and their address as a copy chip.
type peersPanel struct {
	ui *UI

	items  []peerItem
	header *widget.RichText
	list   *widget.List
	root   fyne.CanvasObject
}

func newPeersPanel(u *UI) *peersPanel {
	pp := &peersPanel{ui: u}
	pp.header = newEyebrow("People")

	pp.list = widget.NewList(
		func() int { return len(pp.items) },
		func() fyne.CanvasObject { return newPeerRow(u) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(pp.items) {
				return
			}
			obj.(*peerRow).set(pp.items[id])
		},
	)
	pp.list.HideSeparators = true
	pp.list.OnSelected = func(widget.ListItemID) { pp.list.UnselectAll() }

	pp.root = newSurface(container.NewBorder(
		container.New(layout.NewCustomPaddedLayout(12, 2, 14, 14), pp.header),
		nil, nil, nil,
		container.New(layout.NewCustomPaddedLayout(0, 6, 4, 4), pp.list),
	), colorNameShaft)
	return pp
}

func (pp *peersPanel) refresh(snap client.Snapshot) {
	items := make([]peerItem, 0, len(snap.Peers)+1)
	items = append(items, peerItem{
		name:      snap.SelfPeer.Name + " (you)",
		virtualIP: snap.SelfPeer.VirtualIP,
		detail:    plural(len(snap.LocalShares), "port shared", "ports shared"),
		dot:       theme.ColorNamePrimary,
	})
	for _, p := range snap.Peers {
		mode, dot := describeMode(p.Mode)
		parts := []string{mode}
		if rtt := formatRTT(p.RTT); rtt != "" {
			parts = append(parts, rtt)
		}
		items = append(items, peerItem{
			name:      p.Name,
			virtualIP: p.VirtualIP,
			detail:    strings.Join(parts, " · "),
			dot:       dot,
		})
	}

	pp.items = items
	setEyebrow(pp.header, fmt.Sprintf("People · %d", len(items)))
	pp.list.Refresh()
}

// describeMode turns a transport mode into short user-facing text and an LED color.
func describeMode(m client.TransportMode) (string, fyne.ThemeColorName) {
	switch m {
	case client.TransportDirect:
		return "Direct", theme.ColorNameSuccess
	case client.TransportRelay:
		return "Via relay", colorNameRelay
	case client.TransportOffline:
		return "Offline", theme.ColorNameError
	default:
		return "Connecting…", theme.ColorNamePlaceHolder
	}
}

type peerRow struct {
	widget.BaseWidget

	dot    *statusDot
	name   *widget.Label
	detail *widget.RichText
	chip   *copyChip
}

func newPeerRow(u *UI) *peerRow {
	r := &peerRow{
		dot:    newStatusDot(),
		name:   newTitle(),
		detail: newMutedText(theme.SizeNameCaptionText),
		chip: newCopyChip(theme.SizeNameCaptionText, theme.ColorNameForeground, func(v string) {
			u.copyToClipboard(v, "address")
		}),
	}
	r.ExtendBaseWidget(r)
	return r
}

func (r *peerRow) set(item peerItem) {
	r.dot.set(item.dot)
	if r.name.Text != item.name {
		r.name.SetText(item.name)
	}
	setMutedText(r.detail, item.detail, "")
	r.chip.SetValue(item.virtualIP, false)
}

func (r *peerRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.New(layout.NewCustomPaddedLayout(2, 2, 8, 4),
		container.NewBorder(nil, nil,
			r.dot.root,
			container.NewCenter(r.chip),
			twoLines(r.name, r.detail))))
}
