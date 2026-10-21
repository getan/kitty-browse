// Package tuios provides the optional, best-effort integration with a tuios
// pane. It never affects a browser run outside tuios.
package tuios

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Pane is the tuios pane this process runs in, when any.
type Pane struct {
	ID      string
	Binary  string
	Session string

	enabled bool
	run     func(context.Context, []string) error
}

// FromEnv detects a tuios pane. A missing variable or binary disables the
// integration rather than failing the browser.
func FromEnv(getenv func(string) string) *Pane {
	if getenv == nil {
		getenv = os.Getenv
	}
	p := &Pane{
		ID:      strings.TrimSpace(getenv("TUIOS_PANE_ID")),
		Session: strings.TrimSpace(getenv("TUIOS_SESSION")),
	}
	p.run = func(ctx context.Context, argv []string) error {
		cmd := exec.CommandContext(ctx, p.Binary, argv...)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		return cmd.Run()
	}
	if getenv("TUIOS_ENV") != "1" || getenv("TUIOS_SOCKET") == "" || p.ID == "" {
		return p
	}
	bin, err := exec.LookPath("tuios")
	if err != nil {
		return p
	}
	p.Binary = bin
	p.enabled = true
	return p
}

// Enabled reports whether a pane and the tuios executable were found.
func (p *Pane) Enabled() bool { return p != nil && p.enabled }

// Rename is best-effort. It never blocks a browser run for more than a short
// timeout and ignores errors: the title is cosmetic.
func (p *Pane) Rename(title string) {
	if !p.Enabled() {
		return
	}
	title = strings.TrimSpace(strings.ReplaceAll(title, "\n", " "))
	if len(title) > 80 {
		r := []rune(title)
		if len(r) > 80 {
			title = string(r[:78]) + "…"
		}
	}
	p.runAsync([]string{"set-window", "-w", p.ID, "--name", title})
}

// Clear restores the pane's shell-derived name.
func (p *Pane) Clear() {
	if !p.Enabled() {
		return
	}
	p.runAsync([]string{"set-window", "-w", p.ID, "--name", ""})
}

func (p *Pane) runAsync(argv []string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.run(ctx, argv)
	}()
}
