package tui

import "github.com/charmbracelet/lipgloss"

// Adaptive colors keep the UI readable on both light and dark terminals.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	colUser   = lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#38BDF8"}
	colTool   = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	colErr    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#94A3B8", Dark: "#64748B"}
	colBuild  = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
)

type styles struct {
	modePlan    lipgloss.Style
	modeBuild   lipgloss.Style
	userLabel   lipgloss.Style
	assistLabel lipgloss.Style
	tool        lipgloss.Style
	toolErr     lipgloss.Style
	system      lipgloss.Style
	err         lipgloss.Style
	muted       lipgloss.Style
	inputBox    lipgloss.Style
	status      lipgloss.Style
}

func newStyles() styles {
	return styles{
		modePlan:    lipgloss.NewStyle().Bold(true).Foreground(colUser),
		modeBuild:   lipgloss.NewStyle().Bold(true).Foreground(colBuild),
		userLabel:   lipgloss.NewStyle().Bold(true).Foreground(colUser),
		assistLabel: lipgloss.NewStyle().Bold(true).Foreground(colAccent),
		tool:        lipgloss.NewStyle().Foreground(colTool),
		toolErr:     lipgloss.NewStyle().Foreground(colErr),
		system:      lipgloss.NewStyle().Foreground(colMuted).Italic(true),
		err:         lipgloss.NewStyle().Foreground(colErr),
		muted:       lipgloss.NewStyle().Foreground(colMuted),
		inputBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colMuted).
			Padding(0, 1),
		status: lipgloss.NewStyle().Foreground(colMuted),
	}
}
