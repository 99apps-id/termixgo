// Package ui is the terminal front end: a Bubble Tea program that renders the
// welcome screen, the transcript, the composer and the setup wizard.
package ui

import "github.com/charmbracelet/lipgloss"

// Palette is one colour scheme. Colors are hex strings resolved by the
// terminal, which keeps the UI readable without a 256-colour assumption.
type Palette struct {
	Accent    lipgloss.Color
	Accent2   lipgloss.Color
	Text      lipgloss.Color
	Dim       lipgloss.Color
	Faint     lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color
	Error     lipgloss.Color
	User      lipgloss.Color
	Thinking  lipgloss.Color
	Tool      lipgloss.Color
	Border    lipgloss.Color
	Selection lipgloss.Color
}

// DefaultPalette is the Termixgo dark theme: a cyan-to-violet accent on a
// near-black background.
func DefaultPalette() Palette {
	return Palette{
		Accent:    lipgloss.Color("#5eead4"),
		Accent2:   lipgloss.Color("#a78bfa"),
		Text:      lipgloss.Color("#e5e7eb"),
		Dim:       lipgloss.Color("#9ca3af"),
		Faint:     lipgloss.Color("#4b5563"),
		Success:   lipgloss.Color("#4ade80"),
		Warning:   lipgloss.Color("#fbbf24"),
		Error:     lipgloss.Color("#f87171"),
		User:      lipgloss.Color("#7dd3fc"),
		Thinking:  lipgloss.Color("#c084fc"),
		Tool:      lipgloss.Color("#facc15"),
		Border:    lipgloss.Color("#374151"),
		Selection: lipgloss.Color("#1f2937"),
	}
}

// Styles bundles the render styles derived from a palette.
type Styles struct {
	Palette Palette

	Banner       lipgloss.Style
	BannerAlt    lipgloss.Style
	Title        lipgloss.Style
	Subtitle     lipgloss.Style
	User         lipgloss.Style
	Assistant    lipgloss.Style
	Thinking     lipgloss.Style
	Reasoned     lipgloss.Style
	Tool         lipgloss.Style
	ToolDone     lipgloss.Style
	ToolError    lipgloss.Style
	Plan         lipgloss.Style
	Notice       lipgloss.Style
	Error        lipgloss.Style
	Dim          lipgloss.Style
	Code         lipgloss.Style
	Heading      lipgloss.Style
	Bullet       lipgloss.Style
	StatusBar    lipgloss.Style
	StatusKey    lipgloss.Style
	StatusValue  lipgloss.Style
	StatusTrust  lipgloss.Style
	StatusWarn   lipgloss.Style
	Composer     lipgloss.Style
	ComposerBusy lipgloss.Style
	Prompt       lipgloss.Style
	Menu         lipgloss.Style
	MenuSelected lipgloss.Style
	MenuKey      lipgloss.Style
	MenuDesc     lipgloss.Style
	Box          lipgloss.Style
	BoxTitle     lipgloss.Style
	Button       lipgloss.Style
	ButtonOn     lipgloss.Style
	ButtonOff    lipgloss.Style
	Hint         lipgloss.Style
	Trust        lipgloss.Style
	NoTrust      lipgloss.Style
}

// NewStyles builds the style set for a palette.
func NewStyles(palette Palette) Styles {
	return Styles{
		Palette:      palette,
		Banner:       lipgloss.NewStyle().Foreground(palette.Accent).Bold(true),
		BannerAlt:    lipgloss.NewStyle().Foreground(palette.Accent2).Bold(true),
		Title:        lipgloss.NewStyle().Foreground(palette.Text).Bold(true),
		Subtitle:     lipgloss.NewStyle().Foreground(palette.Dim),
		User:         lipgloss.NewStyle().Foreground(palette.User).Bold(true),
		Assistant:    lipgloss.NewStyle().Foreground(palette.Text),
		Thinking:     lipgloss.NewStyle().Foreground(palette.Thinking).Italic(true),
		Reasoned:     lipgloss.NewStyle().Foreground(palette.Faint).Italic(true),
		Tool:         lipgloss.NewStyle().Foreground(palette.Tool),
		ToolDone:     lipgloss.NewStyle().Foreground(palette.Success),
		ToolError:    lipgloss.NewStyle().Foreground(palette.Error),
		Plan:         lipgloss.NewStyle().Foreground(palette.Accent),
		Notice:       lipgloss.NewStyle().Foreground(palette.Dim),
		Error:        lipgloss.NewStyle().Foreground(palette.Error),
		Dim:          lipgloss.NewStyle().Foreground(palette.Dim),
		Code:         lipgloss.NewStyle().Foreground(palette.Accent).Background(lipgloss.Color("#111827")),
		Heading:      lipgloss.NewStyle().Foreground(palette.Text).Bold(true),
		Bullet:       lipgloss.NewStyle().Foreground(palette.Accent),
		StatusBar:    lipgloss.NewStyle().Foreground(palette.Dim),
		StatusKey:    lipgloss.NewStyle().Foreground(palette.Faint),
		StatusValue:  lipgloss.NewStyle().Foreground(palette.Text),
		StatusTrust:  lipgloss.NewStyle().Foreground(palette.Success).Bold(true),
		StatusWarn:   lipgloss.NewStyle().Foreground(palette.Warning).Bold(true),
		Composer:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Border).Padding(0, 1),
		ComposerBusy: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Accent2).Padding(0, 1),
		Prompt:       lipgloss.NewStyle().Foreground(palette.Accent2).Bold(true),
		Menu:         lipgloss.NewStyle().Padding(0, 1),
		MenuSelected: lipgloss.NewStyle().Foreground(palette.Accent).Bold(true),
		MenuKey:      lipgloss.NewStyle().Foreground(palette.Accent2),
		MenuDesc:     lipgloss.NewStyle().Foreground(palette.Dim),
		Box:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Border).Padding(1, 2),
		BoxTitle:     lipgloss.NewStyle().Foreground(palette.Accent).Bold(true),
		Button:       lipgloss.NewStyle().Foreground(palette.Text).Padding(0, 1),
		ButtonOn:     lipgloss.NewStyle().Foreground(lipgloss.Color("#052e2b")).Background(palette.Accent).Padding(0, 1).Bold(true),
		ButtonOff:    lipgloss.NewStyle().Foreground(palette.Text).Background(palette.Selection).Padding(0, 1),
		Hint:         lipgloss.NewStyle().Foreground(palette.Faint),
		Trust:        lipgloss.NewStyle().Foreground(palette.Success).Bold(true),
		NoTrust:      lipgloss.NewStyle().Foreground(palette.Warning).Bold(true),
	}
}
