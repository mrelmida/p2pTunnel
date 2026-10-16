package ui

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

type peerRowItem struct {
	isSelf     bool
	name       string
	virtualIP  string
	mode       string
	rttText    string
	portsCount int
}

type peersPanel struct {
	ui *UI

	mu   sync.RWMutex
	rows []peerRowItem

	list        *widget.List
	emptyBanner *widget.Label
	root        fyne.CanvasObject
}

func newPeersPanel(u *UI) *peersPanel {
	pp := &peersPanel{ui: u}

	pp.emptyBanner = widget.NewLabelWithStyle(
		"Join or create a room to see connected peers and their assigned 127.0.0.x IPs.",
		fyne.TextAlignCenter,
		fyne.TextStyle{Italic: true},
	)
	pp.emptyBanner.Wrapping = fyne.TextWrapWord

	pp.list = widget.NewList(
		func() int {
			pp.mu.RLock()
			defer pp.mu.RUnlock()
			return len(pp.rows)
		},
		func() fyne.CanvasObject {
			nameLbl := widget.NewLabelWithStyle("Peer Name", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			ipLbl := widget.NewLabelWithStyle("127.0.0.2", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
			modeLbl := widget.NewLabel("Direct P2P • 12 ms")
			copyBtn := widget.NewButtonWithIcon("Copy IP", theme.ContentCopyIcon(), nil)

			left := container.NewVBox(
				container.NewHBox(nameLbl, ipLbl),
				modeLbl,
			)
			return container.NewBorder(nil, nil, nil, copyBtn, left)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			pp.mu.RLock()
			if id < 0 || id >= len(pp.rows) {
				pp.mu.RUnlock()
				return
			}
			item := pp.rows[id]
			pp.mu.RUnlock()

			border := obj.(*fyne.Container)
			left := border.Objects[0].(*fyne.Container)
			copyBtn := border.Objects[1].(*widget.Button)

			topRow := left.Objects[0].(*fyne.Container)
			nameLbl := topRow.Objects[0].(*widget.Label)
			ipLbl := topRow.Objects[1].(*widget.Label)
			modeLbl := left.Objects[1].(*widget.Label)

			nameLbl.SetText(item.name)
			ipLbl.SetText(fmt.Sprintf("[%s]", item.virtualIP))
			if item.isSelf {
				modeLbl.SetText(fmt.Sprintf("Local Host (You) • %d port(s) shared", item.portsCount))
			} else {
				modeLbl.SetText(fmt.Sprintf("%s • %s • %d port(s) shared", item.mode, item.rttText, item.portsCount))
			}

			ipToCopy := item.virtualIP
			copyBtn.OnTapped = func() {
				pp.ui.win.Clipboard().SetContent(ipToCopy)
				pp.ui.setFooterStatus(fmt.Sprintf("Copied peer IP %s to clipboard", ipToCopy))
			}
		},
	)

	pp.root = widget.NewCard(
		"Room Peers & Virtual IPs",
		"Each peer gets a dedicated 127.0.0.x address",
		container.NewStack(pp.list, pp.emptyBanner),
	)
	return pp
}

func (pp *peersPanel) refresh(snap client.Snapshot) {
	var nextRows []peerRowItem
	if snap.Connected {
		nextRows = append(nextRows, peerRowItem{
			isSelf:     true,
			name:       fmt.Sprintf("%s (You)", snap.SelfPeer.Name),
			virtualIP:  snap.SelfPeer.VirtualIP,
			mode:       "Local",
			rttText:    "0 ms",
			portsCount: len(snap.LocalShares),
		})
		for _, p := range snap.Peers {
			rttStr := "—"
			if p.RTT > 0 {
				ms := float64(p.RTT) / float64(time.Millisecond)
				if ms < 1.0 {
					rttStr = "<1 ms"
				} else {
					rttStr = fmt.Sprintf("%.0f ms", ms)
				}
			}
			nextRows = append(nextRows, peerRowItem{
				isSelf:     false,
				name:       p.Name,
				virtualIP:  p.VirtualIP,
				mode:       string(p.Mode),
				rttText:    rttStr,
				portsCount: len(p.SharedPorts),
			})
		}
	}

	pp.mu.Lock()
	pp.rows = nextRows
	pp.mu.Unlock()

	if len(nextRows) == 0 {
		pp.emptyBanner.Show()
		pp.list.Hide()
	} else {
		pp.emptyBanner.Hide()
		pp.list.Show()
	}
	pp.list.Refresh()
}
