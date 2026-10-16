package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"p2pTunnel/internal/client"
)

// logWindow is a separate, on-demand window showing the activity log, so
// troubleshooting detail doesn't take space on the main screen.
type logWindow struct {
	ui *UI

	lines   []string
	lastSeq uint64

	win     fyne.Window
	list    *widget.List
	visible bool
}

func newLogWindow(u *UI) *logWindow {
	return &logWindow{ui: u}
}

func (lw *logWindow) show() {
	if lw.win == nil {
		lw.build()
	}
	lw.visible = true
	lw.list.Refresh()
	lw.list.ScrollToBottom()
	lw.win.Show()
	lw.win.RequestFocus()
}

func (lw *logWindow) build() {
	lw.win = lw.ui.app.NewWindow("p2pTunnel — Activity log")
	lw.win.Resize(fyne.NewSize(760, 420))
	lw.win.SetCloseIntercept(func() {
		lw.visible = false
		lw.win.Hide()
	})

	lw.list = widget.NewList(
		func() int { return len(lw.lines) },
		func() fyne.CanvasObject {
			l := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(lw.lines) {
				return
			}
			l := obj.(*widget.Label)
			l.Importance = logLineImportance(lw.lines[id])
			l.SetText(lw.lines[id])
		},
	)
	lw.list.HideSeparators = true
	lw.list.OnSelected = func(widget.ListItemID) { lw.list.UnselectAll() }

	copyBtn := widget.NewButtonWithIcon("Copy all", theme.ContentCopyIcon(), func() {
		lw.ui.app.Clipboard().SetContent(strings.Join(lw.lines, "\n"))
		lw.ui.flash("Copied the activity log to the clipboard")
	})
	lw.win.SetContent(container.NewBorder(nil,
		container.NewPadded(container.NewBorder(nil, nil, nil, copyBtn)),
		nil, nil, lw.list))
}

func (lw *logWindow) refresh(snap client.Snapshot) {
	if snap.LogSeq == lw.lastSeq {
		return
	}
	lw.lastSeq = snap.LogSeq
	lw.lines = snap.Logs
	if lw.visible {
		lw.list.Refresh()
		lw.list.ScrollToBottom()
	}
}

// logLineImportance colors failures red and successful connections green.
func logLineImportance(line string) widget.Importance {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "fail"), strings.Contains(lower, "error"),
		strings.Contains(lower, "cannot"), strings.Contains(lower, "unable"):
		return widget.DangerImportance
	case strings.Contains(lower, "connected to"):
		return widget.SuccessImportance
	default:
		return widget.MediumImportance
	}
}
