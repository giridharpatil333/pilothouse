package tea

// This directory is github.com/charmbracelet/bubbletea v1.3.10, used via a
// replace directive in Pilothouse's go.mod. The only change: tea_init.go is
// removed. Upstream's init() calls lipgloss.HasDarkBackground(), which
// queries the terminal (OSC 11) in every program that links Bubble Tea —
// so every pilothouse command, including the hook shim whose stdout is the
// agent's protocol, waited up to 5 s on terminals that never answer, and a
// late reply leaked into the next program's input. internal/tui makes its
// own bounded query right before starting a Program instead. Upstream
// removes the init in v2; drop this fork when moving to it.
