package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

// roomView is the screen shown while in a room: a header plate with the room
// code and your address, the people list on the left, and tabs for ports
// shared with you and ports you share.
type roomView struct {
	ui *UI

	codeChip *copyChip
	ipChip   *copyChip
	summary  *widget.RichText

	peers  *peersPanel
	remote *remotePortsPanel
	local  *localSharesPanel

	tabs      *container.AppTabs
	remoteTab *container.TabItem
	localTab  *container.TabItem

	root fyne.CanvasObject
}

func newRoomView(u *UI) *roomView {
	rv := &roomView{ui: u}

	rv.codeChip = newCopyChip(theme.SizeNameSubHeadingText, theme.ColorNamePrimary, func(v string) {
		u.copyToClipboard(v, "room code")
	})
	rv.ipChip = newCopyChip(theme.SizeNameSubHeadingText, theme.ColorNameForeground, func(v string) {
		u.copyToClipboard(v, "your address")
	})
	rv.summary = newMutedText(theme.SizeNameCaptionText)
	rv.summary.Truncation = fyne.TextTruncateOff

	leaveBtn := widget.NewButtonWithIcon("Leave room", theme.LogoutIcon(), func() {
		u.userLeft = true
		go u.mgr.LeaveRoom()
	})

	// Amber signal bar down the left edge of the plate.
	bar := canvas.NewRectangle(theme.Color(theme.ColorNamePrimary))
	bar.CornerRadius = 2
	bar.SetMinSize(fyne.NewSize(4, 0))

	plateContent := container.NewBorder(nil, nil,
		container.NewHBox(
			labelled("Room", container.NewHBox(rv.codeChip)),
			hGap(20),
			labelled("Your address", container.NewHBox(rv.ipChip)),
		),
		container.NewHBox(container.NewCenter(rv.summary), container.NewCenter(leaveBtn)),
	)
	plate := newSurface(container.NewBorder(nil, nil,
		container.New(layout.NewCustomPaddedLayout(14, 14, 14, 0), bar),
		nil,
		container.New(layout.NewCustomPaddedLayout(10, 10, 12, 14), plateContent),
	), colorNameSurface)

	rv.peers = newPeersPanel(u)
	rv.remote = newRemotePortsPanel(u)
	rv.local = newLocalSharesPanel(u)

	rv.remoteTab = container.NewTabItemWithIcon("Shared with you", theme.DownloadIcon(), rv.remote.root)
	rv.localTab = container.NewTabItemWithIcon("Your shared ports", theme.UploadIcon(), rv.local.root)
	rv.tabs = container.NewAppTabs(rv.remoteTab, rv.localTab)

	split := container.NewHSplit(rv.peers.root, rv.tabs)
	split.Offset = 0.31

	rv.root = container.NewBorder(
		container.New(layout.NewCustomPaddedLayout(12, 8, 12, 12), plate),
		nil, nil, nil,
		container.New(layout.NewCustomPaddedLayout(0, 10, 12, 12), split),
	)
	return rv
}

func (rv *roomView) refresh(snap client.Snapshot) {
	rv.codeChip.SetValue(snap.RoomCode, false)
	rv.ipChip.SetValue(snap.SelfPeer.VirtualIP, false)
	setMutedText(rv.summary, roomSummary(snap), "")

	rv.peers.refresh(snap)
	rv.remote.refresh(snap)
	rv.local.refresh(snap)

	remoteText := tabTitle("Shared with you", len(snap.RemoteBindings))
	localText := tabTitle("Your shared ports", len(snap.LocalShares))
	if rv.remoteTab.Text != remoteText || rv.localTab.Text != localText {
		rv.remoteTab.Text = remoteText
		rv.localTab.Text = localText
		rv.tabs.Refresh()
	}
}

// roomSummary describes the room at a glance, e.g. "3 people · all direct".
func roomSummary(snap client.Snapshot) string {
	people := plural(len(snap.Peers)+1, "person", "people")
	if len(snap.Peers) == 0 {
		return people + " · waiting for others"
	}
	var direct, relay int
	for _, p := range snap.Peers {
		switch p.Mode {
		case client.TransportDirect:
			direct++
		case client.TransportRelay:
			relay++
		}
	}
	switch {
	case direct == len(snap.Peers):
		return people + " · all direct"
	case relay > 0:
		return fmt.Sprintf("%s · %d via relay", people, relay)
	default:
		return people + " · connecting"
	}
}

func tabTitle(text string, n int) string {
	if n == 0 {
		return text
	}
	return fmt.Sprintf("%s (%d)", text, n)
}
