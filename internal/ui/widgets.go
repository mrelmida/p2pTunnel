package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// newMutedText creates a single-line, ellipsis-truncated text drawn in the
// theme's placeholder color, for secondary details under a title.
func newMutedText(size fyne.ThemeSizeName) *widget.RichText {
	rt := widget.NewRichText(&widget.TextSegment{
		Style: widget.RichTextStyle{
			ColorName: theme.ColorNamePlaceHolder,
			SizeName:  size,
			Inline:    true,
		},
	})
	rt.Truncation = fyne.TextTruncateEllipsis
	return rt
}

// setMutedText updates text created by newMutedText, refreshing only on change.
// An empty colorName keeps the default placeholder color.
func setMutedText(rt *widget.RichText, text string, colorName fyne.ThemeColorName) {
	if colorName == "" {
		colorName = theme.ColorNamePlaceHolder
	}
	seg := rt.Segments[0].(*widget.TextSegment)
	if seg.Text == text && seg.Style.ColorName == colorName {
		return
	}
	seg.Text = text
	seg.Style.ColorName = colorName
	rt.Refresh()
}

// newEyebrow creates the small uppercase label that names a field or panel.
func newEyebrow(text string) *widget.RichText {
	rt := widget.NewRichText(&widget.TextSegment{
		Text: strings.ToUpper(text),
		Style: widget.RichTextStyle{
			ColorName: theme.ColorNamePlaceHolder,
			SizeName:  theme.SizeNameCaptionText,
			TextStyle: fyne.TextStyle{Bold: true},
			Inline:    true,
		},
	})
	return rt
}

// setEyebrow updates text created by newEyebrow.
func setEyebrow(rt *widget.RichText, text string) {
	seg := rt.Segments[0].(*widget.TextSegment)
	if text = strings.ToUpper(text); seg.Text != text {
		seg.Text = text
		rt.Refresh()
	}
}

// labelled stacks an eyebrow label tightly above a field.
func labelled(label string, field fyne.CanvasObject) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedVBoxLayout(-theme.InnerPadding()/2), newEyebrow(label), field)
}

// twoLines stacks a row title over its detail line with tighter spacing
// than a plain VBox (both widgets already carry their own inner padding).
func twoLines(title, detail fyne.CanvasObject) *fyne.Container {
	return container.New(layout.NewCustomPaddedVBoxLayout(-theme.InnerPadding()), title, detail)
}

// newTitle creates a bold, ellipsis-truncated label for list row titles.
func newTitle() *widget.Label {
	l := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

// newEmptyState creates the centered icon + message shown when a list is empty.
func newEmptyState(icon fyne.Resource, title, body string) fyne.CanvasObject {
	img := canvas.NewImageFromResource(theme.NewColoredResource(icon, colorNameSeam))
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(44, 44))

	t := widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	b := widget.NewRichText(&widget.TextSegment{
		Text: body,
		Style: widget.RichTextStyle{
			Alignment: fyne.TextAlignCenter,
			ColorName: theme.ColorNamePlaceHolder,
		},
	})
	b.Wrapping = fyne.TextWrapWord
	return container.New(&maxWidthLayout{width: 380, center: true},
		container.NewVBox(container.NewCenter(img), t, b))
}

// hGap is fixed horizontal space, for boxes where a Spacer would collapse.
func hGap(w float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(w, 0))
	return r
}

// newIconButton creates a compact, low-emphasis icon-only button.
func newIconButton(icon fyne.Resource, tapped func()) *widget.Button {
	b := widget.NewButtonWithIcon("", icon, tapped)
	b.Importance = widget.LowImportance
	return b
}

// newSurface wraps content in a rounded, softly bordered panel.
func newSurface(content fyne.CanvasObject, fill fyne.ThemeColorName) *fyne.Container {
	bg := canvas.NewRectangle(theme.Color(fill))
	bg.CornerRadius = theme.Size(theme.SizeNameCardRadius)
	bg.StrokeColor = theme.Color(colorNameSeam)
	bg.StrokeWidth = 1
	return container.NewStack(bg, content)
}

// ---------------------------------------------------------------------------
// Link LED: a small glowing dot showing a peer's connection state.
// ---------------------------------------------------------------------------

type statusDot struct {
	led  *canvas.Rectangle
	name fyne.ThemeColorName
	root fyne.CanvasObject
}

const ledSize = 10

func newStatusDot() *statusDot {
	led := canvas.NewRectangle(theme.Color(theme.ColorNamePlaceHolder))
	led.CornerRadius = ledSize / 2
	d := &statusDot{led: led}
	d.root = container.NewCenter(container.NewGridWrap(fyne.NewSize(ledSize, ledSize), led))
	return d
}

func (d *statusDot) set(name fyne.ThemeColorName) {
	if d.name == name {
		return
	}
	d.name = name
	c := theme.Color(name)
	d.led.FillColor = c
	// A coloured, offset-free shadow reads as the LED's glow.
	r, g, b, _ := c.RGBA()
	glow := color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xC0}
	if name == theme.ColorNamePlaceHolder {
		glow.A = 0 // "connecting" is dim, no glow
	}
	d.led.Shadow = canvas.Shadow{Color: glow, BlurRadius: 10, Spread: 1.5, Variant: canvas.BoxShadow}
	d.led.Refresh()
}

// ---------------------------------------------------------------------------
// copyChip: a value on an inset pill that copies itself when clicked.
// ---------------------------------------------------------------------------

type copyChip struct {
	widget.BaseWidget

	bg     *canvas.Rectangle
	text   *canvas.Text
	copied *canvas.Text
	icon   *widget.Icon

	value       string
	textColor   fyne.ThemeColorName
	disabled    bool
	hovered     bool
	copiedUntil time.Time

	onCopy func(value string)
}

// newCopyChip creates a chip with monospace text of the given size and color.
func newCopyChip(size fyne.ThemeSizeName, textColor fyne.ThemeColorName, onCopy func(string)) *copyChip {
	c := &copyChip{textColor: textColor, onCopy: onCopy}
	c.bg = canvas.NewRectangle(theme.Color(colorNameInset))
	c.bg.CornerRadius = theme.Size(theme.SizeNameInputRadius)
	c.bg.StrokeWidth = 1

	c.text = canvas.NewText("", theme.Color(textColor))
	c.text.TextStyle = fyne.TextStyle{Monospace: true}
	c.text.TextSize = theme.Size(size)

	c.copied = canvas.NewText("Copied", theme.Color(theme.ColorNameSuccess))
	c.copied.TextStyle = fyne.TextStyle{Bold: true}
	c.copied.TextSize = theme.Size(theme.SizeNameCaptionText)
	c.copied.Alignment = fyne.TextAlignCenter
	c.copied.Hide()

	c.icon = widget.NewIcon(theme.NewColoredResource(theme.ContentCopyIcon(), theme.ColorNamePlaceHolder))
	c.ExtendBaseWidget(c)
	c.applyState()
	return c
}

// SetValue updates the displayed value; a disabled chip is dimmed and inert.
func (c *copyChip) SetValue(value string, disabled bool) {
	if c.value == value && c.disabled == disabled {
		return
	}
	c.value = value
	c.disabled = disabled
	c.applyState()
}

func (c *copyChip) applyState() {
	copied := time.Now().Before(c.copiedUntil)
	c.text.Text = c.value
	switch {
	case copied:
		// Keep the value laid out (just invisible) so the chip keeps its width.
		c.text.Color = color.Transparent
	case c.disabled:
		c.text.Color = theme.Color(theme.ColorNameDisabled)
	default:
		c.text.Color = theme.Color(c.textColor)
	}
	if copied {
		c.copied.Show()
	} else {
		c.copied.Hide()
	}
	if c.hovered && !c.disabled {
		c.bg.StrokeColor = theme.Color(theme.ColorNamePrimary)
	} else {
		c.bg.StrokeColor = theme.Color(colorNameSeam)
	}
	if c.disabled {
		c.icon.Hide()
	} else {
		c.icon.Show()
	}
	c.text.Refresh()
	c.copied.Refresh()
	c.bg.Refresh()
}

func (c *copyChip) Tapped(*fyne.PointEvent) {
	if c.disabled || c.value == "" {
		return
	}
	if c.onCopy != nil {
		c.onCopy(c.value)
	}
	c.copiedUntil = time.Now().Add(1200 * time.Millisecond)
	c.applyState()
	time.AfterFunc(1300*time.Millisecond, func() { fyne.Do(c.applyState) })
}

func (c *copyChip) MouseIn(*desktop.MouseEvent) {
	c.hovered = true
	c.applyState()
}

func (c *copyChip) MouseMoved(*desktop.MouseEvent) {}

func (c *copyChip) MouseOut() {
	c.hovered = false
	c.applyState()
}

func (c *copyChip) Cursor() desktop.Cursor {
	if c.disabled {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

func (c *copyChip) CreateRenderer() fyne.WidgetRenderer {
	iconBox := container.NewGridWrap(fyne.NewSquareSize(theme.Size(theme.SizeNameInlineIcon)*0.8), c.icon)
	// The "Copied" text overlays the value so the chip keeps its width.
	body := container.NewHBox(container.NewStack(container.NewCenter(c.text), c.copied), container.NewCenter(iconBox))
	return widget.NewSimpleRenderer(container.NewStack(c.bg,
		container.New(layout.NewCustomPaddedLayout(5, 5, 10, 8), body)))
}

// ---------------------------------------------------------------------------
// Layout helpers and formatting.
// ---------------------------------------------------------------------------

// maxWidthLayout gives its children at most `width` horizontally and centers
// them, so forms and empty states don't stretch across a wide window.
// With center set, children are also centered vertically at their min height.
type maxWidthLayout struct {
	width  float32
	center bool
}

func (l *maxWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	w := min(size.Width, l.width)
	for _, o := range objects {
		h := size.Height
		y := float32(0)
		if l.center {
			// Fix the width first: wrapped text only reports its true
			// height once it knows how wide it is.
			o.Resize(fyne.NewSize(w, o.Size().Height))
			h = min(o.MinSize().Height, size.Height)
			y = (size.Height - h) / 2
		}
		o.Resize(fyne.NewSize(w, h))
		o.Move(fyne.NewPos((size.Width-w)/2, y))
	}
}

func (l *maxWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var s fyne.Size
	for _, o := range objects {
		s = s.Max(o.MinSize())
	}
	return s
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

func formatRTT(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < time.Millisecond {
		return "<1 ms"
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

// plural returns "1 thing" / "2 things".
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}
