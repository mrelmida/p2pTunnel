package ui

import (
	_ "embed"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The look is "tunnel lighting": a cool night-navy lit by warm sodium-vapour
// amber, the colour of real road-tunnel lamps. Amber is the single accent
// (primary actions, the room code, focus). Link status gets its own LED
// colours: mint for direct, sky for relayed, coral for offline.
//
// The theme is always dark, regardless of the OS light/dark setting.

// Fonts: Fira Sans (designed with signage in mind) for interface text and
// JetBrains Mono for addresses and codes. Both are SIL OFL 1.1; the licence
// texts sit next to the font files in assets/fonts.
var (
	//go:embed assets/fonts/FiraSans-Regular.ttf
	firaRegular []byte
	//go:embed assets/fonts/FiraSans-SemiBold.ttf
	firaSemiBold []byte
	//go:embed assets/fonts/FiraSans-Italic.ttf
	firaItalic []byte
	//go:embed assets/fonts/JetBrainsMono-Medium.ttf
	monoMedium []byte
	//go:embed assets/fonts/JetBrainsMono-Bold.ttf
	monoBold []byte

	//go:embed assets/logo.svg
	logoSVG []byte
)

var (
	fontRegular  = fyne.NewStaticResource("FiraSans-Regular.ttf", firaRegular)
	fontBold     = fyne.NewStaticResource("FiraSans-SemiBold.ttf", firaSemiBold)
	fontItalic   = fyne.NewStaticResource("FiraSans-Italic.ttf", firaItalic)
	fontMono     = fyne.NewStaticResource("JetBrainsMono-Medium.ttf", monoMedium)
	fontMonoBold = fyne.NewStaticResource("JetBrainsMono-Bold.ttf", monoBold)

	logoResource = fyne.NewStaticResource("p2ptunnel-logo.svg", logoSVG)
)

// Palette.
var (
	colTunnelNight = rgb(0x0D, 0x13, 0x1F) // window background
	colShaft       = rgb(0x11, 0x19, 0x27) // sidebar, status bar
	colConcrete    = rgb(0x17, 0x20, 0x30) // cards and panels
	colLining      = rgb(0x1F, 0x2A, 0x3D) // inputs and raised controls
	colSeam        = rgb(0x2A, 0x37, 0x4E) // borders and separators
	colSodium      = rgb(0xFF, 0xB5, 0x47) // the accent
	colMint        = rgb(0x4A, 0xDE, 0x9B) // direct link
	colSky         = rgb(0x6C, 0xB4, 0xFF) // relayed link
	colCoral       = rgb(0xFF, 0x6B, 0x6B) // errors, offline
	colFog         = rgb(0xE6, 0xEA, 0xF2) // primary text
	colMist        = rgb(0x8A, 0x97, 0xAD) // secondary text
	colSlate       = rgb(0x4A, 0x56, 0x6B) // disabled text
)

// App-specific colour names, resolved by tunnelTheme.
const (
	colorNameSurface fyne.ThemeColorName = "p2p-surface" // cards and panels
	colorNameShaft   fyne.ThemeColorName = "p2p-shaft"   // sidebar, status bar
	colorNameInset   fyne.ThemeColorName = "p2p-inset"   // address chips
	colorNameSeam    fyne.ThemeColorName = "p2p-seam"    // panel borders
	colorNameRelay   fyne.ThemeColorName = "p2p-relay"   // relayed-link LED
)

type tunnelTheme struct{}

var _ fyne.Theme = (*tunnelTheme)(nil)

func (tunnelTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colTunnelNight
	case colorNameShaft:
		return colShaft
	case colorNameSurface, theme.ColorNameOverlayBackground, theme.ColorNameHeaderBackground:
		return colConcrete
	case theme.ColorNameMenuBackground:
		return colLining
	case colorNameInset, theme.ColorNameInputBackground, theme.ColorNameButton:
		return colLining
	case theme.ColorNameDisabledButton:
		return colConcrete
	case colorNameSeam, theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return colSeam
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		return colSodium
	case theme.ColorNameFocus:
		return withAlpha(colSodium, 0x88)
	case theme.ColorNameSelection:
		return withAlpha(colSodium, 0x40)
	case theme.ColorNameSuccess:
		return colMint
	case colorNameRelay:
		return colSky
	case theme.ColorNameWarning:
		return rgb(0xFF, 0xD1, 0x66)
	case theme.ColorNameError:
		return colCoral
	case theme.ColorNameForeground:
		return colFog
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnSuccess,
		theme.ColorNameForegroundOnWarning, theme.ColorNameForegroundOnError:
		return colTunnelNight
	case theme.ColorNamePlaceHolder:
		return colMist
	case theme.ColorNameDisabled:
		return colSlate
	case theme.ColorNameHover:
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x0F}
	case theme.ColorNamePressed:
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x1C}
	case theme.ColorNameScrollBar:
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x30}
	case theme.ColorNameScrollBarBackground:
		return color.NRGBA{A: 0x00}
	case theme.ColorNameShadow:
		return color.NRGBA{A: 0x70}
	}
	return theme.DefaultTheme().Color(name, theme.VariantDark)
}

func (tunnelTheme) Font(style fyne.TextStyle) fyne.Resource {
	switch {
	case style.Symbol:
		return theme.DefaultSymbolFont()
	case style.Monospace && style.Bold:
		return fontMonoBold
	case style.Monospace:
		return fontMono
	case style.Bold:
		return fontBold
	case style.Italic:
		return fontItalic
	}
	return fontRegular
}

func (tunnelTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (tunnelTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 14
	case theme.SizeNameCaptionText:
		return 12
	case theme.SizeNameSubHeadingText:
		return 18
	case theme.SizeNameHeadingText:
		return 28
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius, theme.SizeNameSelectionRadius:
		return 8
	case theme.SizeNameCardRadius, theme.SizeNameDialogRadius:
		return 14
	case theme.SizeNamePopupRadius, theme.SizeNameMenuRadius:
		return 10
	case theme.SizeNameScrollBarRadius:
		return 4
	case theme.SizeNameSeparatorThickness, theme.SizeNameInputBorder:
		return 1
	}
	return theme.DefaultTheme().Size(name)
}

func rgb(r, g, b uint8) color.NRGBA {
	return color.NRGBA{R: r, G: g, B: b, A: 0xFF}
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}
