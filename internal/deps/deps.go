//go:build tools

// Package deps pins the module's dependencies so parallel workstreams never
// need to edit go.mod. Remove an import here once real code uses it.
package deps

import (
	_ "cel.dev/cel-go/cel"
	_ "github.com/SherClockHolmes/webpush-go"
	_ "github.com/charmbracelet/bubbles/list"
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/lipgloss"
	_ "github.com/creack/pty"
	_ "github.com/mdp/qrterminal/v3"
	_ "golang.org/x/sys/unix"
	_ "golang.org/x/term"
	_ "modernc.org/sqlite"
)
