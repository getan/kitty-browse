// Package browsepane runs the visible kitty-browse pane: a real Chromium page
// drawn into a terminal that supports the Kitty graphics protocol (or
// iTerm2/Sixel through rasterm).
//
// Run owns the whole visible experience — tab strip, toolbar, mouse and
// keyboard input, the native action control socket — and returns when the
// person quits, the control API asks the pane to quit, or ctx is canceled. It
// never calls os.Exit, never touches flag.CommandLine and installs no signal
// handler that outlives it, so a host application such as golder can serve the
// same pane from its own binary.
//
// Run reads stdin only while it runs: it stops and joins its input reader on
// every exit path, and it never closes the streams the caller handed it, so the
// caller reads its own stdin again the moment the pane is gone.
package browsepane

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	api "github.com/getan/kitty-browse/internal/action"
	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/control"
	"github.com/getan/kitty-browse/internal/history"
	"github.com/getan/kitty-browse/internal/host"
	"github.com/getan/kitty-browse/internal/render"
	"github.com/getan/kitty-browse/internal/screenshot"
	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/tuios"
	"github.com/getan/kitty-browse/internal/ui"
)

// paneQuitGrace keeps the control socket alive just long enough for the reply
// to a quit action to reach the client that asked for it.
const paneQuitGrace = 200 * time.Millisecond

// debugLog is this run's optional debug logger. It is nil unless
// KITTY_BROWSE_DEBUG named a file, and it never writes to the terminal the pane
// is drawing into.
var debugLog atomic.Pointer[log.Logger]

func debugf(format string, args ...any) {
	if logger := debugLog.Load(); logger != nil {
		logger.Printf(format, args...)
	}
}

// runPane owns one visible browser. It never calls os.Exit: every exit path
// returns an error (or nil) after the deferred teardown has closed the renderer,
// Chromium, the control socket and the terminal.
func runPane(ctx context.Context, opts Options) error {
	in, out, errOut := opts.Stdin, opts.Stdout, opts.Stderr
	flags := flag.NewFlagSet(opts.Name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	var (
		fps         = flags.Float64("fps", 24, "maximum frames per second (0.1..240)")
		scrollLines = flags.Float64("scroll-lines", 2, "terminal cell heights per wheel step (0.1..100)")
		quality     = flags.Int("quality", 85, "JPEG quality, 0-100")
		maxWidth    = flags.Int("width", 0, "maximum frame width in pixels; 0 sizes to the pane")
		maxHeight   = flags.Int("height", 0, "maximum frame height in pixels; 0 sizes to the pane")
		load        = flags.Duration("load-timeout", 30*time.Second, "page load timeout")
		measure     = flags.Bool("measure-cells", true, "query cell pixel size asynchronously (disable with -measure-cells=false)")
		chrome      = flags.String("chrome", os.Getenv("KITTY_BROWSE_CHROME"), "Chromium executable path (or KITTY_BROWSE_CHROME)")
		socket      = flags.String("control-socket", os.Getenv("KITTY_BROWSE_SOCKET"), "optional owner-only Unix socket for native browser actions and screenshots")
		socketAlias = flags.String("socket", "", "alias for -control-socket")
		shotDir     = flags.String("screenshot-dir", ".local/screenshots", "directory for s/S screenshot hotkeys")
		profileDir  = flags.String("profile-dir", "", "dedicated persistent Chromium profile directory (default: user config directory)")
		downloadDir = flags.String("download-dir", "", "download directory (default: profile Downloads, or temporary in private mode)")
		private     = flags.Bool("private", false, "use a temporary profile and in-memory history; do not retain browsing data")
		stealth     = flags.Bool("stealth", true, "remove Chromium's headless UA token and automation markers (default on; -stealth=false restores the native headless identity)")
		userAgent   = flags.String("user-agent", "", "optional explicit User-Agent; default: the running Chromium desktop UA with the headless token removed")
	)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "usage: %s [flags] [url]\n", opts.Name)
		flags.PrintDefaults()
	}
	if err := flags.Parse(opts.Args); err != nil {
		return helpAsSuccess(err)
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return errors.New("pass flags before the single optional URL")
	}
	visited := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["socket"] {
		if visited["control-socket"] && *socket != *socketAlias {
			return errors.New("-socket and -control-socket must name the same path")
		}
		*socket = *socketAlias
	}

	interval, err := frameInterval(*fps)
	if err != nil || !validScrollLines(*scrollLines) {
		return errors.New("fps must be finite and 0.1..240; scroll-lines must be finite and 0.1..100")
	}

	if path := os.Getenv("KITTY_BROWSE_DEBUG"); path != "" && !*private {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			debugLog.Store(log.New(f, "", log.Lmicroseconds))
			defer func() {
				debugLog.Store(nil)
				_ = f.Close()
			}()
		}
	}
	debugf("start target=%q", debugURL(flags.Arg(0)))

	target := strings.TrimSpace(flags.Arg(0))
	if target == "" {
		target = "https://example.com"
	}
	browser.PruneStalePrivateProfiles(time.Now())

	var browserOutput io.Writer
	startup := &startupOnlyWriter{writer: opts.StartupOutput}
	defer startup.stop()
	phase := func(p string) {
		if opts.StartupPhase != nil {
			opts.StartupPhase(p)
		}
	}
	if opts.StartupOutput != nil && !*private {
		browserOutput = startup
	}
	if logger := debugLog.Load(); logger != nil {
		if browserOutput != nil {
			browserOutput = io.MultiWriter(browserOutput, chromiumLogWriter{logger: logger})
		} else {
			browserOutput = chromiumLogWriter{logger: logger}
		}
		debugf("build %s fps=%g quality=%d maxViewport=%dx%d", opts.Version, *fps, *quality, *maxWidth, *maxHeight)
	}
	phase("starting_chromium")
	browserCtx, closeAlloc, profile, err := browser.NewBrowserContext(ctx, browser.LaunchOptions{
		ExecutablePath: *chrome, ProfileDir: *profileDir, DownloadDir: *downloadDir, Private: *private,
		Stealth: *stealth, UserAgent: *userAgent, BrowserOutput: browserOutput,
		RequireSandbox: opts.RequireChromiumSandbox,
	})
	if err != nil {
		return err
	}
	defer closeAlloc()
	debugf("starting browser executable=%q profile=%q private=%v stealth=%v", *chrome, profile.Dir, profile.Private, *stealth)
	tabs, err := browser.OpenTabsWithReady(browserCtx, target, *load, func() { startup.stop(); phase("browser_connected") })
	if err != nil {
		return err
	}
	defer tabs.Close()
	sess := tabs.Active()
	logBrowserDiagnostics(sess)
	if err := sess.LoadError(); err != nil {
		// The browser stays on Chromium's error page; say why once, since the
		// page itself is all the person sees.
		debugf("initial page: %v", err)
	}
	debugf("browser open url=%s", debugURL(sess.URL()))

	pane := tuios.FromEnv(nil)
	defer pane.Clear()

	store := history.NewMemory()
	if !profile.Private {
		if loaded, err := history.Open(profile.StatePath); err == nil {
			store = loaded
		} else {
			debugf("profile history unavailable: %v", err)
		}
	}
	_ = store.Record(sess.URL())

	phase("initializing_terminal")
	screen, err := term.Open(in, out)
	if err != nil {
		return err
	}
	defer screen.Close()
	debugf("screen open")
	kittyGraphics := term.QueryKittyGraphics(in, out)
	debugf("kitty graphics probe=%v", kittyGraphics)

	rows, cols := screen.Size()
	pixels := term.TerminalCellPixels(in, rows, cols)
	debugf("cell pixels=%+v", pixels)
	geom := paneGeometry(rows, cols, pixels, *maxWidth, *maxHeight)
	debugf("viewport geometry=%+v", geom)
	phase("initializing_renderer")
	if err := sess.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx); err != nil {
		debugf("set viewport error: %v", err)
		screen.Header("kitty-browse: viewport: " + err.Error())
		time.Sleep(2 * time.Second)
		return fmt.Errorf("initialize viewport: %w", err)
	}
	debugf("viewport set")

	stream, err := startTabStream(sess, geom, *fps, *quality, kittyGraphics)
	if err != nil {
		debugf("render start error: %v", err)
		screen.Header("kitty-browse: " + err.Error())
		time.Sleep(2 * time.Second)
		if errors.Is(err, render.ErrNoGraphics) {
			return errors.New("this terminal has no Kitty/iTerm2/Sixel graphics support")
		}
		return err
	}
	defer func() { stream.Stop() }()
	defer func() { _ = screen.WriteBody(stream.Clear) }()
	debugf("render transport started; waiting for first image target=%s", sess.TargetID())

	model := ui.New(sess.URL())
	model.Private = profile.Private
	tabModels := []*ui.Model{model}
	titles := map[*browser.Session]string{}
	cacheTabTitle(titles, sess)
	syncTabChrome(model, tabs, titles)
	bridge := host.NewBridge("browser-" + newActionRequestID())
	bridge.Update(tabs, titles)
	refreshLists(model, store)
	updatePaneTitle(pane, sess)
	drawChrome(screen, model, store)

	events := newInputQueue(64)
	// The pump owns its own cancelable context and interruptible reader, so
	// every exit path below stops reading and joins the goroutine before the
	// pane returns and the host application reads stdin again.
	input, err := startInput(ctx, in, events)
	if err != nil {
		return err
	}
	defer input.stop()
	if *measure {
		screen.QueryPixels()
	}
	resize := opts.Resize
	if resize == nil {
		if signals := resizeSignals(); len(signals) != 0 {
			own := make(chan os.Signal, 1)
			signal.Notify(own, signals...)
			defer signal.Stop(own)
			resize = own
		}
	}
	tick := time.NewTicker(min(interval, 100*time.Millisecond))
	defer tick.Stop()
	stateTick := time.NewTicker(time.Second)
	defer stateTick.Stop()
	var lastImage image.Image
	var lastStill time.Time
	var lastStillFor time.Time
	var haveStill bool
	var reportedRenderError bool
	var reportedScrollError bool
	var receivedFrames int
	var resizePending bool
	var probe stillProbe
	defer probe.stop()
	health := newRenderHealth(time.Now())
	var renderStatus renderNotice
	renderStatus.set(&model.Status, "Waiting for first page image...")
	drawChrome(screen, model, store)
	resetRenderHealth := func() {
		probe.stop()
		renderStatus.clear(&model.Status)
		health = newRenderHealth(time.Now())
		haveStill = false
		lastStill, lastStillFor = time.Time{}, time.Time{}
	}
	showRenderFailure := func() {
		if health.exhausted {
			debugf("render unavailable; recovery exhausted target=%s restarts=%d failures=%d stats=%+v",
				sess.TargetID(), health.restarts, health.failures, stream.Stats())
			renderStatus.set(&model.Status, renderUnavailable)
			// No image has ever been shown: replace the unexplained black area
			// with a useful message. Otherwise keep the last good image.
			if lastImage == nil && !model.TextMode {
				screen.Text([]string{"Page image unavailable.", "Automatic recovery stopped after 2 attempts.", "Reload or switch tabs to retry."})
			}
		} else if health.waitingTooLong(time.Now()) {
			renderStatus.set(&model.Status, "No first page image yet; checking renderer...")
		} else {
			renderStatus.set(&model.Status, "Page image capture failed; checking renderer...")
		}
		drawChrome(screen, model, store)
	}
	frameReady := func(source string) {
		if health.frame() {
			debugf("render ready source=%s target=%s viewport=%dx%d restarts=%d stats=%+v",
				source, sess.TargetID(), geom.ContentWidthPx, geom.ContentHeightPx, health.restarts, stream.Stats())
			renderStatus.clear(&model.Status)
			drawChrome(screen, model, store)
		}
	}
	// fitWidth and fitHeight widen the virtual viewport when a page's layout is
	// wider than the pane; see fitPageWidth.
	var fitWidth, fitHeight int
	// fitRetries re-measures for a few state ticks after a viewport or page
	// change: a reflow caused by the change itself can make an immediate
	// measurement report the old width.
	var fitRetries int
	// A page press owns the pointer until release, so a slider can be dragged
	// and a text selection can be extended.
	var dragging bool
	var dragCol, dragRow int
	var dragX, dragY float64
	scroller := browser.NewScroller(sess.Context(), sess.ScrollContext)
	defer func() { scroller.Stop() }()
	resetScroller := func() {
		scroller.Stop()
		scroller = browser.NewScroller(sess.Context(), sess.ScrollContext)
		reportedScrollError = false
	}

	applyViewport := func() error {
		probe.stop()
		scroller.Stop()
		width, height := *maxWidth, *maxHeight
		if width == 0 && fitWidth > 0 {
			width, height = fitWidth, fitHeight
		}
		next := paneGeometry(rows, cols, pixels, width, height)
		if err := sess.SetViewport(next.ContentWidthPx, next.ContentHeightPx); err != nil {
			model.Status = err.Error()
			debugf("viewport error: %v", err)
			resetScroller()
			return err
		}
		geom = next
		stream.SetViewportSize(geom.ContentWidthPx, geom.ContentHeightPx)
		stream.SetCellSize(cols, geom.ContentRows)
		resetScroller()
		// A still captured for the old viewport has the old pixel dimensions and
		// the old cell box. Without forcing a new capture, a grown pane keeps the
		// smaller image anchored at the top, leaving a gap at the bottom that
		// reads as "the page shifted up".
		haveStill, lastStill, lastStillFor = false, time.Time{}, time.Time{}
		debugf("updated geometry=%+v cell pixels=%+v", geom, pixels)
		if model.TextMode {
			screen.Text(model.TextView(screen.BodyRows(), cols))
		}
		drawChrome(screen, model, store)
		return nil
	}
	// fitPageWidth widens the virtual viewport when a fixed-width desktop page
	// would otherwise be clipped by the pane. Width and height grow together so
	// the raster keeps the pane's aspect ratio: terminals scale a Kitty placement
	// into its cell box, and a mismatched aspect would letterbox the page and
	// desynchronize clicks. The image is then scaled back into the same cells, so
	// the whole page stays visible.
	fitPageWidth := func() {
		if *maxWidth > 0 || *maxHeight > 0 || bridge.Active.Load() > 0 {
			return
		}
		base := paneGeometry(rows, cols, pixels, 0, 0)
		measureCtx, cancel := context.WithTimeout(sess.Context(), time.Second)
		pageWidth, err := sess.LayoutWidth(measureCtx)
		cancel()
		if err != nil {
			return
		}
		width, height, ok := fitViewportSize(base.ContentWidthPx, base.ContentHeightPx, pageWidth)
		if !ok || (width == fitWidth && height == fitHeight) {
			return
		}
		fitWidth, fitHeight = width, height
		debugf("fit page width=%d height=%d pane=%dx%d", fitWidth, fitHeight, base.ContentWidthPx, base.ContentHeightPx)
		if err := applyViewport(); err != nil {
			fitWidth, fitHeight = 0, 0
		}
	}
	activateTab := func() error {
		probe.stop()
		nextSession := tabs.Active()
		nextModel := tabModels[tabs.Index()]
		if err := nextSession.SetViewport(geom.ContentWidthPx, geom.ContentHeightPx); err != nil {
			return err
		}
		// Stop before starting: the old StopScreencast would otherwise stop the
		// new stream too when an action reselects the current target.
		stream.Stop()
		next, err := startTabStream(nextSession, geom, *fps, *quality, kittyGraphics)
		if err != nil {
			return err
		}
		sess, model = nextSession, nextModel
		stream = next
		lastImage = nil
		lastStill, lastStillFor = time.Time{}, time.Time{}
		haveStill, reportedRenderError = false, false
		resetRenderHealth()
		renderStatus.set(&model.Status, "Waiting for first page image...")
		receivedFrames = 0
		cacheTabTitle(titles, sess)
		syncTabChrome(model, tabs, titles)
		refreshLists(model, store)
		updatePaneTitle(pane, sess)
		if model.TextMode {
			screen.Text(model.TextView(screen.BodyRows(), cols))
		}
		fitPageWidth()
		fitRetries = 2
		return nil
	}

	quit := host.NewQuit()
	if *socket != "" {
		server, err := control.Start(*socket, tabs.Screenshot,
			control.WithAPI(bridge.Information, bridge.Execute),
			control.WithQuit(func(context.Context) { quit.Request() }))
		if err != nil {
			return err
		}
		defer server.Close()
		debugf("native action control socket=%s browser=%s", *socket, bridge.ID)
	}
	phase("ready")

	handleHostAction := func(call host.Call) {
		view := bridge.Load()
		target, targetErr := api.SelectTarget(view.Info, call.Req.Target)
		result, actionErr := func() (json.RawMessage, error) {
			if call.Ctx.Err() != nil {
				return nil, call.Ctx.Err()
			}
			var p struct {
				URL    string `json:"url"`
				TabID  string `json:"tabId"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			}
			if len(call.Req.Params) != 0 && json.Unmarshal(call.Req.Params, &p) != nil {
				return nil, clientError("invalid_arguments", "invalid host action parameters")
			}
			if browserScopedAction(call.Req.Action) {
				switch call.Req.Action {
				case "info", "tab.list":
					return json.Marshal(view.Info)
				case "capabilities":
					return json.Marshal(api.Capabilities())
				case "done":
					return host.ResultDone, nil
				case "quit":
					// The reply is written after this handler returns; the loop
					// then keeps the socket alive for a moment so the client that
					// asked for quit always receives it.
					quit.Request()
					return host.ResultQuitting, nil
				case "tab.new":
					if call.Ctx.Err() != nil {
						return nil, call.Ctx.Err()
					}
					scroller.Stop()
					if err := host.NewTab(call.Ctx, tabs, p.URL, *load); err != nil {
						resetScroller()
						return nil, err
					}
					fresh := ui.New(tabs.Active().URL())
					fresh.Private = profile.Private
					tabModels = append(tabModels, fresh)
					if err := activateTab(); err != nil {
						return nil, err
					}
					resetScroller()
				}
				syncTabChrome(model, tabs, titles)
				bridge.Update(tabs, titles)
				target, _ = api.SelectTarget(bridge.Load().Info, api.Target{})
				return json.Marshal(bridge.Load().Info)
			}
			if targetErr != nil {
				return nil, targetErr
			}
			index := target.Index - 1
			selected, sessionErr := host.SessionFor(tabs, target)
			if sessionErr != nil {
				return nil, sessionErr
			}
			switch call.Req.Action {
			case "tab.activate":
				if call.Ctx.Err() != nil {
					return nil, call.Ctx.Err()
				}
				scroller.Stop()
				if err := host.SelectTab(tabs, index); err != nil {
					resetScroller()
					return nil, err
				}
				if err := activateTab(); err != nil {
					return nil, err
				}
				resetScroller()
			case "tab.close":
				if call.Ctx.Err() != nil {
					return nil, call.Ctx.Err()
				}
				scroller.Stop()
				sessions := tabs.Sessions()
				if err := host.CloseTab(tabs, index); err != nil {
					resetScroller()
					return nil, err
				}
				delete(titles, sessions[index])
				tabModels = append(tabModels[:index], tabModels[index+1:]...)
				if err := activateTab(); err != nil {
					return nil, err
				}
				resetScroller()
			case "set.viewport":
				if call.Ctx.Err() != nil {
					return nil, call.Ctx.Err()
				}
				if index != tabs.Index() {
					return nil, clientError("target_not_active", "activate the selected tab before changing its viewport")
				}
				if err := host.CheckViewport(p.Width, p.Height); err != nil {
					return nil, err
				}
				oldWidth, oldHeight := *maxWidth, *maxHeight
				*maxWidth, *maxHeight = p.Width, p.Height
				if err := applyViewport(); err != nil {
					*maxWidth, *maxHeight = oldWidth, oldHeight
					return nil, err
				}
				return json.Marshal(map[string]int{"width": geom.ContentWidthPx, "height": geom.ContentHeightPx})
			case "navigate", "back", "forward", "reload":
				scroller.Stop()
				err := host.Navigate(call.Ctx, selected, call.Req.Action, p.URL, *load)
				if selected == sess {
					resetRenderHealth()
				}
				resetScroller()
				if err != nil {
					return nil, err
				}
				tabModels[index].SetURL(selected.URL())
				cacheTabTitle(titles, selected)
				recordVisit(store, tabModels[index], selected.URL())
			default:
				return nil, clientError("unsupported_action", "unsupported host action")
			}
			syncTabChrome(model, tabs, titles)
			bridge.Update(tabs, titles)
			if updated, err := api.SelectTarget(bridge.Load().Info, api.Target{TargetID: target.TargetID}); err == nil {
				target = updated
			}
			return json.Marshal(bridge.Load().Info)
		}()
		bridge.Update(tabs, titles)
		syncTabChrome(model, tabs, titles)
		drawChrome(screen, model, store)
		call.Reply <- api.Respond(call.Req, bridge.Load().Info, target, result, actionErr)
	}

	fitPageWidth()
	fitRetries = 2

	for {
		select {
		case <-ctx.Done():
			debugf("ctx done")
			return nil
		case <-quit.Done():
			debugf("control quit")
			return nil
		case call := <-bridge.Calls:
			probe.stop()
			handleHostAction(call)
			if quit.Requested() {
				// The quit reply is queued for the HTTP handler that asked for
				// it; keep the socket alive until that write has happened.
				debugf("quit action")
				select {
				case <-time.After(paneQuitGrace):
				case <-ctx.Done():
				}
				return nil
			}
		case <-resize:
			rows, cols = screen.Refresh()
			pixels.WidthPx, pixels.HeightPx = cols*pixels.CellW, rows*pixels.CellH
			if bridge.Active.Load() > 0 {
				resizePending = true
				continue
			}
			fitWidth, fitHeight = 0, 0
			_ = applyViewport()
			fitPageWidth()
			fitRetries = 2
			if *measure {
				screen.QueryPixels()
			}
		case <-stateTick.C:
			if debugLog.Load() != nil {
				debugf("perf input=%+v scroll=%+v render=%+v", events.Stats(), scroller.Stats(), stream.Stats())
			}
			if scroller.Busy() || events.Stats().Pending != 0 {
				continue // Don't compete with a gesture for CDP round trips.
			}
			if bridge.Active.Load() > 0 {
				continue
			}
			if resizePending {
				resizePending = false
				_ = applyViewport()
				if *measure {
					screen.QueryPixels()
				}
			}
			if fitRetries > 0 {
				fitRetries--
				fitPageWidth()
			}
			before := tabs.Sessions()
			if changed, err := tabs.Reconcile(ctx); err == nil && changed {
				// Tabs an external client closed are gone from the tab set, so
				// their cached titles go with them.
				pruneTabTitles(titles, tabs.Sessions())
				remembered := make(map[*browser.Session]*ui.Model)
				for i, old := range before {
					if i < len(tabModels) {
						remembered[old] = tabModels[i]
					}
				}
				tabModels = nil
				for _, item := range tabs.Sessions() {
					saved := remembered[item]
					if saved == nil {
						saved = ui.New(item.URL())
						saved.Private = profile.Private
					}
					tabModels = append(tabModels, saved)
				}
				scroller.Stop()
				if err := activateTab(); err != nil {
					debugf("reconcile active tab: %v", err)
					return nil
				}
				resetScroller()
				drawChrome(screen, model, store)
			}
			oldTitle, oldBack, oldForward := titles[sess], model.CanGoBack, model.CanGoForward
			if title, err := sess.Sync(ctx); err == nil {
				titles[sess] = title
				changedURL := model.URL != sess.URL()
				if changedURL {
					model.SetURL(sess.URL())
					recordVisit(store, model, sess.URL())
				}
				syncTabChrome(model, tabs, titles)
				if changedURL || title != oldTitle || oldBack != model.CanGoBack || oldForward != model.CanGoForward {
					updatePaneTitle(pane, sess)
					drawChrome(screen, model, store)
				}
				if changedURL {
					fitPageWidth()
					fitRetries = 2
				}
			}
			bridge.Update(tabs, titles)
		case <-tick.C:
			if model.AgentActive != (bridge.Active.Load() > 0) {
				model.AgentActive = bridge.Active.Load() > 0
				drawChrome(screen, model, store)
			}
			if err := scroller.Err(); err != nil && !reportedScrollError {
				model.Status = err.Error()
				debugf("scroll error: %v", err)
				drawChrome(screen, model, store)
				reportedScrollError = true
			}
			if err := stream.Err(); err != nil && !reportedRenderError {
				debugf("render transport error target=%s stats=%+v: %v", sess.TargetID(), stream.Stats(), err)
				health.transportFailed()
				showRenderFailure()
				reportedRenderError = true
			}
			if model.TextMode || scroller.Busy() || events.Stats().Pending != 0 {
				probe.stop()
				continue
			}
			if bridge.Active.Load() > 0 || health.exhausted {
				continue // don't interfere with a native action, or retry forever
			}
			if health.recoveryDue(time.Now()) {
				probe.stop()
				health.restarting(time.Now())
				renderStatus.set(&model.Status, fmt.Sprintf("Recovering page image (%d/%d)...", health.restarts, maxRenderRestarts))
				drawChrome(screen, model, store)
				debugf("render recovery attempt=%d/%d target=%s viewport=%dx%d stats=%+v",
					health.restarts, maxRenderRestarts, sess.TargetID(), geom.ContentWidthPx, geom.ContentHeightPx, stream.Stats())
				// Restart only image transport, never the browser or document.
				scroller.Stop()
				stream.Stop()
				next, err := startTabStream(sess, geom, *fps, *quality, kittyGraphics)
				if err != nil {
					debugf("render recovery failed target=%s: %v", sess.TargetID(), err)
					health.transportFailed()
					showRenderFailure()
				} else {
					stream = next
					reportedRenderError, haveStill = false, false
					receivedFrames = 0
				}
				lastStill = time.Now()
				resetScroller()
				continue
			}
			if health.waitingTooLong(time.Now()) && renderStatus.text != "No first page image yet; checking renderer..." {
				showRenderFailure()
			}
			if last := stream.LastFrame(); probe.result == nil && shouldCaptureStill(time.Now(), last, lastStill, lastStillFor, haveStill, lastImage != nil) {
				// Also seed an initial frame when screencast produces no event.
				// The bounded PNG request runs off the UI goroutine: the first-frame
				// warning, tab controls and native actions remain responsive.
				lastStill = time.Now()
				probe.start(ctx, last, stream.Still)
			}
		case result := <-probe.result:
			probe.stop()
			if result.err != nil {
				debugf("still capture error target=%s viewport=%dx%d stats=%+v: %v",
					sess.TargetID(), geom.ContentWidthPx, geom.ContentHeightPx, stream.Stats(), result.err)
				// Don't restart a working screencast just because a concurrent PNG
				// failed. It may already have supplied an image to the UI.
				if !stream.LastFrame().After(result.started) && len(stream.Frames()) == 0 {
					health.failed()
					showRenderFailure()
				}
			} else if img := result.image; img != nil {
				debugf("still frame %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
				if drawFrame(screen, stream, img, model, store) {
					frameReady("PNG")
				}
				lastImage = img
				lastStillFor, haveStill = result.forFrame, true
			}
		case <-events.Ready():
			ev, ok := events.Pop()
			if !ok {
				continue
			}
			if bridge.Active.Load() > 0 {
				// Keep native actions deterministic: while golder owns the target,
				// human input does not race its ref/coordinate operation. Emergency
				// quit remains available and cancellation tears down the action ctx.
				if dragging {
					dragging = false
					_ = sess.MouseUp(dragX, dragY)
				}
				if ev.Kind == term.KeyCtrlC || ev.Kind == term.KeyCtrlQ {
					return nil
				}
				continue
			}
			if ev.Kind == term.KeyMouse && ev.Mouse.WheelX != 0 {
				continue
			}
			if ev.Kind == term.KeyMouse && ev.Mouse.Wheel != 0 {
				x, y, inside := geom.CellToPixel(ev.Mouse.Col, ev.Mouse.Row)
				if !inside {
					continue
				}
				if model.TextMode {
					model.TextTop = max(model.TextTop+ev.Mouse.Wheel*2, 0)
					screen.Text(model.TextView(screen.BodyRows(), cols))
				} else if model.Overlay == ui.OverlayNone {
					scroller.Push(x, y, scrollPixels(geom, ev.Mouse.Wheel, *scrollLines))
				}
				continue // No synchronous CDP wait and no toolbar redraw per wheel.
			}
			debugf("event kind=%d rune=%q mouse=%+v", ev.Kind, ev.Rune, ev.Mouse)
			if ev.Kind == term.KeyCellPixels {
				if next := mergeCellPixels(pixels, ev.Pixels, rows, cols); next != pixels {
					pixels = next
					_ = applyViewport()
					fitPageWidth()
				}
				continue
			}
			var action ui.Action
			if ev.Kind == term.KeyMouse {
				if dragging {
					// A press owns the pointer until release, even when the drag
					// crosses into the chrome rows.
					x, y, inside := geom.CellToPixelClamped(ev.Mouse.Col, ev.Mouse.Row)
					if !inside {
						continue
					}
					if ev.Mouse.Motion {
						if ev.Mouse.Col == dragCol && ev.Mouse.Row == dragRow {
							continue
						}
						dragCol, dragRow, dragX, dragY = ev.Mouse.Col, ev.Mouse.Row, x, y
						if err := sess.MouseMove(x, y); err != nil {
							debugf("drag move error: %v", err)
						}
						continue
					}
					if !ev.Mouse.Press && ev.Mouse.Button == 0 {
						dragging = false
						if err := sess.MouseUp(x, y); err != nil {
							model.Status = err.Error()
							debugf("mouse up error: %v", err)
							drawChrome(screen, model, store)
						}
					}
					continue
				}
				if ev.Mouse.Motion {
					continue
				}
				if ev.Mouse.Row <= term.ChromeRows {
					action = model.HandleMouse(cols, ev.Mouse)
				} else {
					scroller.Stop()
					if ev.Mouse.Press && ev.Mouse.Button == 0 {
						x, y, inside := geom.CellToPixel(ev.Mouse.Col, ev.Mouse.Row)
						if inside {
							if err := sess.MouseDown(x, y); err != nil {
								model.Status = err.Error()
								debugf("mouse down error: %v", err)
								drawChrome(screen, model, store)
							} else {
								dragging, dragCol, dragRow, dragX, dragY = true, ev.Mouse.Col, ev.Mouse.Row, x, y
							}
						}
					}
					resetScroller()
					continue
				}
			} else {
				// A focused page text field owns typing: printable keys, Backspace,
				// Enter and the arrow keys edit it instead of driving the chrome.
				// Escape leaves the field so the browser shortcuts work again.
				if !chromeOwnsKeys(model) {
					if ev.Kind == term.KeyEscape {
						if focused, err := pageFocused(sess); err == nil && focused {
							if err := sess.BlurFocused(sess.Context()); err != nil {
								debugf("blur page focus: %v", err)
							}
							continue
						}
					} else if pageEditableKeys(ev) && forwardPageKey(sess, ev) {
						continue
					}
				}
				action = model.Handle(ev)
			}
			debugf("action kind=%d url=%s label=%s", action.Kind, debugURL(action.URL), action.Label)
			if interruptsScroll(action.Kind) {
				probe.stop()
				scroller.Stop()
			}
			switch action.Kind {
			case ui.ActionQuit:
				debugf("quit action")
				return nil
			case ui.ActionNewTab:
				if err := tabs.New(ctx, "about:blank", *load); err != nil {
					model.Status = err.Error()
				} else {
					fresh := ui.New("about:blank")
					fresh.Private = profile.Private
					fresh.Editing, fresh.Address = true, ""
					tabModels = append(tabModels, fresh)
					if err := activateTab(); err != nil {
						debugf("activate new tab: %v", err)
						return nil
					}
				}
			case ui.ActionSelectTab:
				if action.Index != tabs.Index() {
					if err := tabs.Select(action.Index); err != nil {
						model.Status = err.Error()
					} else if err := activateTab(); err != nil {
						debugf("select tab: %v", err)
						return nil
					}
				}
			case ui.ActionCloseTab:
				if err := tabs.CloseTab(action.Index); err != nil {
					model.Status = err.Error()
				} else {
					tabModels = append(tabModels[:action.Index], tabModels[action.Index+1:]...)
					if err := activateTab(); err != nil {
						debugf("activate after close: %v", err)
						return nil
					}
				}
			case ui.ActionNavigate:
				if runNavigation(sess, model, action, *load) {
					recordVisit(store, model, sess.URL())
					updatePaneTitle(pane, sess)
					fitPageWidth()
					fitRetries = 2
				}
			case ui.ActionBack, ui.ActionForward, ui.ActionReload:
				if runNavigation(sess, model, action, *load) {
					recordVisit(store, model, sess.URL())
					updatePaneTitle(pane, sess)
					fitPageWidth()
					fitRetries = 2
				}
			case ui.ActionShowHints:
				hints, err := sess.Hints(sess.Context(), 49)
				if err != nil {
					model.Status = err.Error()
				} else {
					model.SetHints(hints)
				}
			case ui.ActionClearHints:
				_ = sess.ClearHints(sess.Context())
				model.ClearHints()
			case ui.ActionHint:
				handleHint(sess, model, action)
			case ui.ActionOpenHistory:
				model.OpenOverlay(ui.OverlayHistory, store.History())
			case ui.ActionOpenBookmarks:
				model.OpenOverlay(ui.OverlayBookmarks, store.Bookmarks())
			case ui.ActionToggleBookmark:
				on, err := store.ToggleBookmark(action.URL)
				if err != nil {
					model.Status = err.Error()
				} else if on {
					model.Status = "bookmarked"
				} else {
					model.Status = "bookmark removed"
				}
				refreshLists(model, store)
			case ui.ActionOverlaySelect:
				if runNavigation(sess, model, ui.Action{Kind: ui.ActionNavigate, URL: action.URL}, *load) {
					recordVisit(store, model, sess.URL())
					updatePaneTitle(pane, sess)
				}
			case ui.ActionOverlayClose:
				model.CloseOverlay()
			case ui.ActionScreenshot, ui.ActionScreenshotFull:
				fullPage := action.Kind == ui.ActionScreenshotFull
				info, err := saveSessionScreenshot(ctx, sess, screenshot.AutoPath(*shotDir, fullPage), fullPage)
				if err != nil {
					model.Status = err.Error()
					debugf("screenshot error: %v", err)
				} else {
					model.Status = "saved " + info.Path
					debugf("screenshot saved path=%s size=%dx%d bytes=%d", info.Path, info.Width, info.Height, info.Bytes)
				}
			case ui.ActionToggleText:
				handleText(screen, stream, model, store, sess, rows, cols, lastImage)
			case ui.ActionTextScroll:
				screen.Text(model.TextView(screen.BodyRows(), cols))
				drawChrome(screen, model, store)
			}
			if interruptsScroll(action.Kind) {
				resetScroller()
			}
			if action.Kind == ui.ActionNavigate || action.Kind == ui.ActionBack || action.Kind == ui.ActionForward || action.Kind == ui.ActionReload || action.Kind == ui.ActionOverlaySelect {
				resetRenderHealth()
				cacheTabTitle(titles, sess)
			}
			syncTabChrome(model, tabs, titles)
			bridge.Update(tabs, titles)
			drawChrome(screen, model, store)
		case img := <-stream.Frames():
			receivedFrames++
			if receivedFrames == 1 {
				debugf("first screencast frame %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
			}
			lastImage = img
			if !model.TextMode {
				// Decode is already frame-paced. Drawing here avoids an extra
				// whole UI-ticker interval of latency after every captured frame.
				if drawFrame(screen, stream, img, model, store) {
					frameReady("screencast")
				}
			}
		}
	}
}

func handleHint(sess *browser.Session, model *ui.Model, action ui.Action) {
	var target browser.Hint
	if action.Hint != nil {
		target = *action.Hint
	}
	_ = sess.ClearHints(sess.Context())
	if target.Label != "" {
		_ = sess.Click(float64(target.CX), float64(target.CY))
	}
	model.ClearHints()
}

func recordVisit(store *history.Store, model *ui.Model, url string) {
	_ = store.Record(url)
	refreshLists(model, store)
}

func refreshLists(model *ui.Model, store *history.Store) {
	model.SetLists(store.History(), store.Bookmarks())
}

func updatePaneTitle(pane *tuios.Pane, sess *browser.Session) {
	if !pane.Enabled() {
		return
	}
	if title, err := sess.Title(sess.Context()); err == nil && title != "" {
		pane.Rename(title)
	}
}

// chromeOwnsKeys reports whether the browser chrome is consuming keyboard input,
// in which case a focused page field must not receive it.
func chromeOwnsKeys(m *ui.Model) bool {
	return m.Editing || m.Overlay != ui.OverlayNone || m.HintMode || m.TextMode
}

// pageEditableKeys reports whether ev is an editing key a focused page field
// should receive instead of the browser chrome.
func pageEditableKeys(ev term.Event) bool {
	switch ev.Kind {
	case term.KeyText, term.KeyBackspace, term.KeyEnter,
		term.KeyUp, term.KeyDown, term.KeyLeft, term.KeyRight:
		return true
	}
	return false
}

// pageFocused reports whether the page's active element accepts text. The check
// is bounded so a busy page cannot stall the input loop.
func pageFocused(sess *browser.Session) (bool, error) {
	ctx, cancel := context.WithTimeout(sess.Context(), 250*time.Millisecond)
	defer cancel()
	return sess.EditableFocus(ctx)
}

// forwardPageKey sends one editing key to a focused page field and reports
// whether the page consumed it. Printable characters go through InsertText so
// composed CJK text arrives as a whole instead of as browser shortcuts.
func forwardPageKey(sess *browser.Session, ev term.Event) bool {
	focused, err := pageFocused(sess)
	if err != nil || !focused {
		return false
	}
	switch ev.Kind {
	case term.KeyText:
		err = sess.InsertText(string(ev.Rune))
	case term.KeyBackspace:
		err = sess.KeyPress("Backspace")
	case term.KeyEnter:
		err = sess.KeyPress("Enter")
	case term.KeyUp:
		err = sess.KeyPress("ArrowUp")
	case term.KeyDown:
		err = sess.KeyPress("ArrowDown")
	case term.KeyLeft:
		err = sess.KeyPress("ArrowLeft")
	case term.KeyRight:
		err = sess.KeyPress("ArrowRight")
	default:
		return false
	}
	if err != nil {
		debugf("page key error: %v", err)
		return false
	}
	return true
}

func runNavigation(sess *browser.Session, model *ui.Model, action ui.Action, timeout time.Duration) bool {
	_ = sess.ClearHints(sess.Context())
	model.ClearHints()
	model.CloseOverlay()
	var err error
	switch action.Kind {
	case ui.ActionNavigate:
		err = sess.Navigate(sess.Context(), action.URL, timeout)
	case ui.ActionBack:
		err = sess.Back(sess.Context(), timeout)
	case ui.ActionForward:
		err = sess.Forward(sess.Context(), timeout)
	case ui.ActionReload:
		err = sess.Reload(sess.Context(), timeout)
	}
	if err != nil {
		model.Status = err.Error()
		return false
	}
	model.SetURL(sess.URL())
	return true
}

func drawFrame(screen *term.Screen, stream *render.Stream, img image.Image, model *ui.Model, store *history.Store) bool {
	if img == nil || screen.BodyRows() == 0 {
		return false
	}
	if err := screen.WriteBody(func(w io.Writer) error {
		return stream.Draw(w, img)
	}); err != nil {
		// A broken pipe or a terminal that went away is a normal exit path.
		debugf("draw frame error: %v", err)
		return false
	}
	// The chrome stays unchanged during a frame update; writing it per frame
	// needlessly makes tuios repaint terminal cells throughout a gesture.
	return true
}

func drawChrome(screen *term.Screen, model *ui.Model, store *history.Store) {
	_, cols := screen.Size()
	screen.Chrome(model.Chrome(cols))
	screen.Footer(model.Footer(store.IsBookmarked(model.URL)))
}

func handleText(screen *term.Screen, stream *render.Stream, model *ui.Model, store *history.Store,
	sess *browser.Session, rows, cols int, last image.Image) {
	if !model.TextMode {
		screen.ClearBody()
		if last != nil {
			drawFrame(screen, stream, last, model, store)
		}
		return
	}
	text, err := sess.Text(sess.Context())
	if err != nil {
		model.Status = err.Error()
		model.TextMode = false
		return
	}
	model.SetText(text)
	_ = screen.WriteBody(stream.Clear)
	screen.Text(model.TextView(screen.BodyRows(), cols))
}

// Upgrade an idle page to PNG once per content change, not once every two
// seconds forever. Before any image has reached the terminal, start the first
// PNG immediately: Chromium may report screencast activity without publishing
// a usable frame, and waiting for the normal idle throttle leaves a black pane
// that a second navigation appears to fix. A failed first probe still sets
// attempt and therefore returns to the bounded two-second retry cadence.
func shouldCaptureStill(now, last, attempt, capturedFor time.Time, haveStill, haveImage bool) bool {
	if !haveImage && attempt.IsZero() {
		return true
	}
	return (last.IsZero() || now.Sub(last) > 400*time.Millisecond) &&
		now.Sub(attempt) > 2*time.Second &&
		(last.IsZero() || !haveStill || !last.Equal(capturedFor))
}
