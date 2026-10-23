package tui

import "github.com/charmbracelet/lipgloss"

// palette uses adaptive colours so the inbox reads well on both dark and
// light terminals. Under NO_COLOR the renderer's profile is Ascii and
// every style collapses to plain text; the layout never relies on colour
// alone (selection has a bar, states have glyphs and words).
var (
	cAccent  = lipgloss.AdaptiveColor{Light: "#5B4BD5", Dark: "#A496FF"}
	cAmber   = lipgloss.AdaptiveColor{Light: "#9A5B00", Dark: "#F5B54A"}
	cGreen   = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#56D364"}
	cBlue    = lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#6CB6FF"}
	cRed     = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#FF7B72"}
	cText    = lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#E6EDF3"}
	cDim     = lipgloss.AdaptiveColor{Light: "#656D76", Dark: "#8B949E"}
	cFaint   = lipgloss.AdaptiveColor{Light: "#AFB8C1", Dark: "#484F58"}
	cSelBg   = lipgloss.AdaptiveColor{Light: "#EAE7FB", Dark: "#262338"}
	cBannerB = lipgloss.AdaptiveColor{Light: "#FFF1D6", Dark: "#3A2A0A"}
)

type styles struct {
	r *lipgloss.Renderer

	brand   lipgloss.Style
	text    lipgloss.Style
	bold    lipgloss.Style
	dim     lipgloss.Style
	faint   lipgloss.Style
	section lipgloss.Style
	count   lipgloss.Style
	rule    lipgloss.Style

	needs   lipgloss.Style
	working lipgloss.Style
	waiting lipgloss.Style
	errored lipgloss.Style
	exited  lipgloss.Style

	here lipgloss.Style
	away lipgloss.Style

	sel    lipgloss.Style // background of the selected row
	selBar lipgloss.Style
	cmd    lipgloss.Style
	label  lipgloss.Style
	key    lipgloss.Style

	ok     lipgloss.Style
	warn   lipgloss.Style
	err    lipgloss.Style
	banner lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	n := r.NewStyle
	return styles{
		r:       r,
		brand:   n().Foreground(cAccent).Bold(true),
		text:    n().Foreground(cText),
		bold:    n().Foreground(cText).Bold(true),
		dim:     n().Foreground(cDim),
		faint:   n().Foreground(cFaint),
		section: n().Foreground(cDim).Bold(true),
		count:   n().Foreground(cFaint),
		rule:    n().Foreground(cFaint),
		needs:   n().Foreground(cAmber).Bold(true),
		working: n().Foreground(cGreen),
		waiting: n().Foreground(cBlue),
		errored: n().Foreground(cRed).Bold(true),
		exited:  n().Foreground(cFaint),
		here:    n().Foreground(cGreen).Bold(true),
		away:    n().Foreground(cAmber).Bold(true),
		sel:     n().Background(cSelBg),
		selBar:  n().Foreground(cAccent).Background(cSelBg).Bold(true),
		cmd:     n().Foreground(cText).Bold(true),
		label:   n().Foreground(cDim),
		key:     n().Foreground(cAccent).Bold(true),
		ok:      n().Foreground(cGreen).Bold(true),
		warn:    n().Foreground(cAmber).Bold(true),
		err:     n().Foreground(cRed).Bold(true),
		banner:  n().Foreground(cAmber).Background(cBannerB).Bold(true),
	}
}
