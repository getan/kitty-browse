package browsepane

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/getan/kitty-browse/browserhost"
	"github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/history"
	"github.com/getan/kitty-browse/internal/host"
	"github.com/getan/kitty-browse/internal/render"
	"github.com/getan/kitty-browse/internal/screenshot"
	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/ui"
)

type remotePane struct {
	view   *browserhost.RemoteView
	info   action.Info
	model  *ui.Model
	screen *term.Screen
	store  *history.Store
}

func (p *remotePane) refresh(ctx context.Context) error {
	raw, err := control.Info(ctx, p.view.Socket)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &p.info); err != nil {
		return err
	}
	if p.info.BrowserID != p.view.BrowserID {
		return errors.New("view endpoint changed identity")
	}
	p.model.Tabs = nil
	for i, tab := range p.info.Tabs {
		p.model.Tabs = append(p.model.Tabs, ui.TabInfo{Title: tab.Title, URL: tab.URL})
		if tab.Active {
			p.model.ActiveTab = i
			if p.model.URL != tab.URL {
				p.model.SetURL(tab.URL)
			}
		}
	}
	return nil
}

func (p *remotePane) chrome(ctx context.Context, name string, params any, tabID string) error {
	target := action.Target{BrowserID: p.view.BrowserID}
	for _, tab := range p.info.Tabs {
		if tab.ID == tabID {
			target.TabID, target.TargetID = tab.ID, tab.TargetID
		}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(action.Request{ID: "operator", Action: name, Params: paramsJSON, Target: target, TimeoutMS: 15000})
	if err != nil {
		return err
	}
	op, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	out, err := p.view.OperatorAction(op, raw)
	if err != nil {
		return err
	}
	var response action.Response
	if err := json.Unmarshal(out, &response); err != nil {
		return err
	}
	if !response.OK {
		if response.Error != nil {
			return response.Error
		}
		return errors.New("operator action failed")
	}
	return nil
}

func (p *remotePane) input(ctx context.Context, in browserhost.OperatorInput) error {
	in.BrowserID, in.TabID = p.view.BrowserID, p.info.ActiveTabID
	for _, tab := range p.info.Tabs {
		if tab.ID == in.TabID {
			in.TargetID = tab.TargetID
		}
	}
	op, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	return p.view.SendOperatorInput(op, in)
}

func (p *remotePane) command(ctx context.Context, command ui.Action) error {
	active := p.info.ActiveTabID
	switch command.Kind {
	case ui.ActionNavigate:
		return p.chrome(ctx, "navigate", map[string]string{"url": command.URL}, active)
	case ui.ActionNewTab:
		return p.chrome(ctx, "tab.new", map[string]string{"url": "about:blank"}, "")
	case ui.ActionSelectTab, ui.ActionCloseTab:
		if command.Index < 0 || command.Index >= len(p.info.Tabs) {
			return errors.New("selected tab is unavailable")
		}
		name := "tab.activate"
		if command.Kind == ui.ActionCloseTab {
			name = "tab.close"
		}
		id := p.info.Tabs[command.Index].ID
		return p.chrome(ctx, name, map[string]string{"tabId": id}, id)
	case ui.ActionBack:
		return p.chrome(ctx, "back", map[string]any{}, active)
	case ui.ActionForward:
		return p.chrome(ctx, "forward", map[string]any{}, active)
	case ui.ActionReload:
		return p.chrome(ctx, "reload", map[string]any{}, active)
	case ui.ActionResumeAutomation:
		p.model.Status = "Resuming automation…"
		p.model.ResumeBusy = true
		drawChrome(p.screen, p.model, p.store)
		req := browserhost.ContinueRequest{BrowserID: p.view.BrowserID, InstanceEpoch: command.InstanceEpoch, PauseGeneration: command.PauseGeneration, RequestID: rand.Text(), ExpiresAt: time.Now().Add(4 * time.Minute)}
		err := p.view.ContinueChecked(ctx, req)
		if err != nil {
			query, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			outcome, queryErr := p.view.ContinuationOutcome(query, req)
			cancel()
			if queryErr == nil && outcome == "resumed" {
				err = nil
			} else if queryErr != nil || outcome != "not_consumed" {
				p.model.ResumeUnknown = true
			}
		}
		p.model.ResumeBusy = false
		if err == nil {
			p.model.Status = "Automation resumed"
			p.model.Interaction.Paused = false
			p.model.Interaction.ResumedGeneration = command.PauseGeneration
		}
		return err
	case ui.ActionKeepPaused:
		p.model.Status = "Automation remains paused"
	case ui.ActionScreenshot, ui.ActionScreenshotFull:
		full := command.Kind == ui.ActionScreenshotFull
		data, err := control.Capture(ctx, p.view.Socket, full)
		if err != nil {
			return err
		}
		_, err = screenshot.Save(screenshot.AutoPath(".local/screenshots", full), data)
		return err
	}
	return nil
}

// RunSharedPane renders one authenticated operator view. It owns neither the
// Chromium process nor another view. Ctrl+Q closes only this view.
func RunSharedPane(ctx context.Context, opts Options, view *browserhost.RemoteView) error {
	opts.normalize()
	if view == nil {
		return errors.New("shared pane requires a view")
	}
	defer view.Close()
	screen, err := term.Open(opts.Stdin, opts.Stdout)
	if err != nil {
		return err
	}
	defer screen.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopLease := view.KeepAlive(ctx)
	defer stopLease()
	kitty := term.QueryKittyGraphics(opts.Stdin, opts.Stdout)
	rows, cols := screen.Size()
	pixels := term.TerminalCellPixels(opts.Stdin, rows, cols)
	geom := paneGeometry(rows, cols, pixels, 0, 0)
	writer, err := render.NewImageWriter(cols, geom.ContentRows, kitty)
	if err != nil {
		return err
	}
	defer func() { _, _ = fmt.Fprint(opts.Stdout, "\x1b_Ga=d,d=A\x1b\\") }()
	p := &remotePane{view: view, screen: screen, model: ui.New(""), store: history.NewMemory()}
	if err := p.refresh(ctx); err != nil {
		return err
	}
	resizeViewport := func() error {
		return p.chrome(ctx, "set.viewport", map[string]int{"width": geom.ContentWidthPx, "height": geom.ContentHeightPx}, p.info.ActiveTabID)
	}
	if err := resizeViewport(); err != nil {
		return err
	}
	events := newInputQueue(64)
	pump, err := startInput(ctx, opts.Stdin, events)
	if err != nil {
		return err
	}
	defer pump.stop()
	screen.QueryPixels()
	resize := make(chan os.Signal, 1)
	if signals := resizeSignals(); len(signals) > 0 {
		signal.Notify(resize, signals...)
		defer signal.Stop(resize)
	}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	type frame struct {
		png    []byte
		target string
		err    error
		status host.InteractionState
	}
	frames := make(chan frame, 1)
	busy := false
	capture := func() {
		if busy {
			return
		}
		busy = true
		tab := p.info.ActiveTabID
		go func() {
			op, stop := context.WithTimeout(ctx, 10*time.Second)
			defer stop()
			data, err := control.Capture(op, view.Socket, false)
			var status host.InteractionState
			raw, _ := json.Marshal(action.Request{ID: "status", Action: "interaction.status", Params: json.RawMessage(`{}`), Target: action.Target{BrowserID: view.BrowserID}})
			if response, e := control.Action(op, view.Socket, raw); e == nil {
				var envelope action.Response
				if json.Unmarshal(response, &envelope) == nil {
					_ = json.Unmarshal(envelope.Result, &status)
				}
			}
			select {
			case frames <- frame{data, tab, err, status}:
			case <-ctx.Done():
			}
		}()
	}
	draw := func() {
		drawChrome(screen, p.model, p.store)
		if !p.model.Interaction.Busy && !p.model.Interaction.Paused && !p.model.Editing && p.model.Status == "" {
			screen.Footer("Manual input ready · Ctrl+L address · Ctrl+Q close view · Ctrl+G resume")
		}
	}
	if opts.StartupPhase != nil {
		opts.StartupPhase("ready")
	}
	draw()
	capture()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := p.refresh(ctx); err != nil {
				return err
			}
			draw()
			capture()
		case f := <-frames:
			busy = false
			if f.status.PauseGeneration > p.model.Interaction.PauseGeneration || f.status.PauseGeneration == p.model.Interaction.PauseGeneration && f.status.ResumedGeneration >= p.model.Interaction.ResumedGeneration {
				if f.status.PauseGeneration > p.model.Interaction.PauseGeneration {
					p.model.ResumeUnknown = false
				}
				p.model.Interaction = f.status
			}
			if f.err != nil {
				p.model.Status = f.err.Error()
				draw()
				continue
			}
			if f.target != p.info.ActiveTabID {
				continue
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(f.png))
			if err != nil || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
				p.model.Status = "Invalid page image"
				draw()
				continue
			}
			img, err := png.Decode(bytes.NewReader(f.png))
			if err != nil {
				return err
			}
			if err := screen.WriteBody(func(out io.Writer) error { return writer.Draw(out, img) }); err != nil {
				return err
			}
			draw()
		case <-resize:
			p.model.CancelFooterPress()
			rows, cols = screen.Refresh()
			geom = paneGeometry(rows, cols, pixels, 0, 0)
			writer, err = render.NewImageWriter(cols, geom.ContentRows, kitty)
			if err != nil {
				return err
			}
			if err := resizeViewport(); err != nil {
				p.model.Status = err.Error()
			}
		case <-events.Ready():
			ev, ok := events.Pop()
			if !ok {
				continue
			}
			if ev.Kind == term.KeyCtrlC || ev.Kind == term.KeyCtrlQ {
				return nil
			}
			if command, handled := p.model.HandleFooterKey(ev); handled {
				if err := p.command(ctx, command); err != nil {
					p.model.Status = err.Error()
				}
				draw()
				continue
			}
			if ev.Kind == term.KeyCellPixels {
				pixels = mergeCellPixels(pixels, ev.Pixels, rows, cols)
				continue
			}
			var command ui.Action
			if ev.Kind == term.KeyMouse && (ev.Mouse.Row == rows || p.model.FooterMouseActive()) {
				command = p.model.HandleFooterMouse(cols, rows, ev.Mouse)
			} else if ev.Kind == term.KeyMouse && ev.Mouse.Row <= term.ChromeRows {
				command = p.model.HandleMouse(cols, ev.Mouse)
			} else if ev.Kind == term.KeyMouse {
				x, y, inside := geom.CellToPixelClamped(ev.Mouse.Col, ev.Mouse.Row)
				if !inside {
					continue
				}
				in := browserhost.OperatorInput{X: x, Y: y}
				switch {
				case ev.Mouse.Wheel != 0:
					in.Kind, in.DeltaY = "scroll", scrollPixels(geom, ev.Mouse.Wheel, 2)
				case ev.Mouse.Motion:
					in.Kind = "pointer-move"
				case ev.Mouse.Press && ev.Mouse.Button == 0:
					in.Kind = "pointer-down"
				case !ev.Mouse.Press && ev.Mouse.Button == 0:
					in.Kind = "pointer-up"
				default:
					continue
				}
				if err := p.input(ctx, in); err != nil {
					p.model.Status = err.Error()
				}
				continue
			} else if p.model.Editing || ev.Kind == term.KeyCtrlL || ev.Kind == term.KeyCtrlR || ev.Kind == term.KeyAltLeft || ev.Kind == term.KeyAltRight {
				command = p.model.Handle(ev)
			} else if in, ok := remotePageKey(ev); ok {
				if err := p.input(ctx, in); err != nil {
					p.model.Status = err.Error()
				}
				continue
			}
			if command.Kind == ui.ActionQuit {
				return nil
			}
			if err := p.command(ctx, command); err != nil {
				p.model.Status = err.Error()
			}
			draw()
		}
	}
}

func remotePageKey(ev term.Event) (browserhost.OperatorInput, bool) {
	if ev.Kind == term.KeyText && ev.Rune != 0 {
		return browserhost.OperatorInput{Kind: "text", Text: string(ev.Rune)}, true
	}
	keys := map[term.EventKind]string{
		term.KeyEnter: "Enter", term.KeyBackspace: "Backspace", term.KeyEscape: "Escape",
		term.KeyUp: "ArrowUp", term.KeyDown: "ArrowDown", term.KeyLeft: "ArrowLeft", term.KeyRight: "ArrowRight", term.KeyTab: "Tab",
	}
	key, ok := keys[ev.Kind]
	return browserhost.OperatorInput{Kind: "key", Key: key}, ok
}
