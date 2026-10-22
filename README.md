# kitty-browse

A small Go terminal browser: real Chromium pixels in Kitty/iTerm2/Sixel
terminals, without the Electron/Rust stack.

> Status: functional prototype. Clickable tabs and navigation/address toolbar,
> mouse, hints, history/bookmarks, screenshots, text fallback and optional tuios
> title reporting are implemented; richer page input controls are future work.

## How it works

```text
chromedp ──CDP screencast──> internal/render ──rasterm/Kitty──> terminal
   │
   └── navigation, clicks, input, JS, cookies, downloads
```

- [`chromedp`](https://github.com/chromedp/chromedp) owns Chromium and exposes
  navigation/automation.
- `internal/render` is a small region-aware renderer derived from
  [`termcast`](https://github.com/chromedp/termcast): it streams Chromium's
  viewport to terminals that speak Kitty, iTerm2 or Sixel graphics, but leaves
  two top rows for a tab strip and navigation/address toolbar.
- The browser lifecycle stays separate so a future `tuios` pane or automation
  tool can reuse the same session.

## Requirements

- Go 1.25+
- Chrome or `chrome-headless-shell`
- A terminal with graphics support: Kitty, Ghostty, WezTerm, iTerm2, or a
  Sixel-capable terminal
- Inside tmux, enable passthrough so the graphics escapes reach the terminal

### Chinese / Unicode and portable fonts

Tab titles, the address field, status and text mode preserve UTF-8. Their layout
uses Unicode grapheme clusters and terminal-cell widths (CJK characters normally
occupy two cells), not byte counts. Truncation and Backspace do not split an emoji
or combining sequence. Untrusted terminal control/bidi formatting characters are
still sanitized; Chinese is not replaced with `?`.

There are **two separate font environments**:

- **TUI labels** are drawn by the host terminal. Keep a normal monospace primary
  font and allow a CJK fallback. macOS normally supplies PingFang SC automatically.
- **Webpage pixels** are drawn by Chromium, using fonts installed where Chromium
  runs. The program respects website CSS/webfonts and system fallback; it does
  not force a font over every page or its icon fonts.

For a common Linux setup, install **Noto CJK**. Debian/Ubuntu:

```bash
sudo apt-get update
sudo apt-get install --no-install-recommends fontconfig fonts-noto-cjk

# The family must actually exist; fc-match alone can silently select another font.
fc-list ':family=Noto Sans CJK SC' family file
fc-list ':family=Noto Sans Mono CJK SC' family file
fc-match -f '%{family[0]} | %{file}\n' 'sans-serif:lang=zh-cn'
```

Other Linux distributions provide equivalent `noto-fonts-cjk`/Noto CJK packages.
For Linux Ghostty, an optional user-selected fallback configuration is:

```ini
font-family = ""
font-family = "JetBrains Mono"
font-family = "Noto Sans Mono CJK SC"
```

The application does not edit terminal/global font settings or bundle a large
font file. With SSH, install the TUI fallback on the **terminal/client machine**
and webpage fonts on the **Chromium/server machine**. Installing one does not
fix missing glyphs in the other. Use a UTF-8 terminal/locale on Linux.

## Run

```bash
go run ./cmd/kitty-browse https://example.com
go run ./cmd/kitty-browse -fps 12 -quality 90 example.com
```

### Project-local Chromium on macOS ARM64

Use an explicit executable path when Chromium is not installed system-wide.
This also works with the executable inside a Chrome, Brave or Edge `.app`.
For the project-local Chromium installation used on this Mac:

```bash
export KITTY_BROWSE_CHROME="$PWD/.local/chromium/chrome-mac/Chromium.app/Contents/MacOS/Chromium"
go run ./cmd/kitty-browse https://example.com
# Or pass -chrome /absolute/path/to/Chromium without setting the environment.
```

The local install is Chromium **158.0.8117.0**, official Mac_Arm snapshot
**1724382** from
`https://storage.googleapis.com/chromium-browser-snapshots/Mac_Arm/1724382/chrome-mac.zip`.
`.local/` is gitignored; no system browser or personal browser profile is used.
Snapshots are development builds, not an automatically updated stable browser.

To verify the whole CDP path without a terminal or external network:

```bash
make test-chromium  # uses KITTY_BROWSE_CHROME from above
go run ./cmd/kitty-browse "file://$PWD/internal/render/testdata/page.html"
```

The test checks rendered JPEG screencast frames, PNG still capture with an
ordinary caller context, and that navigation leaves the browser alive.

## Profiles, private browsing and optional identity compatibility

Normal launches now use a **dedicated persistent Chromium profile**. Nothing is
imported from personal Chrome/Edge/Brave profiles or earlier temporary sessions.
Default locations:

- macOS: `~/Library/Application Support/kitty-browse/profiles/default`
- Linux: `$XDG_CONFIG_HOME/kitty-browse/profiles/default`, normally
  `~/.config/kitty-browse/profiles/default`
- An explicit `XDG_CONFIG_HOME` overrides the base directory on either platform.

Use an independent profile for each account/work context:

```bash
./bin/kitty-browse -profile-dir "$PWD/.local/profiles/work" https://example.com
./bin/kitty-browse -private https://example.com
./bin/kitty-browse -private -stealth https://example.com
```

The profile directory is private (`0700`) and process-locked on macOS/Linux. Two
running instances must not use the same profile; use another directory or private
mode instead. The program never deletes Chromium's own singleton lock to force
access to an occupied profile. Cookies/localStorage and profile-local history /
bookmarks (`kitty-browse-state.json`) can survive a normal close and restart.
Session-only cookies, expiry, logout and server-side session policies still apply:
a persistent profile does not guarantee that every site keeps you logged in.

Treat the profile as login credentials: do not commit it, share it or point the
application at a personal browser's profile. Chromium allocator defaults may use
basic password/mock-keychain storage; filesystem permissions are not a guarantee
of credential encryption at rest. Keep project-local profiles under `.local/`.

`-private` creates an isolated temporary Chromium profile, removed on normal exit.
It neither reads nor writes normal profile cookies or application history and
bookmarks; those application lists are memory-only in private mode. The footer
shows `PRIVATE`, and `KITTY_BROWSE_DEBUG` file logging is disabled. Combining
`-private` with an explicit `-profile-dir` is an error, not an implicit fallback.
Tabs in one private process share that process's private session.

Private mode is **not anonymity or secure erasure**. Temporary browser files may
exist while it runs; crashes/forced termination may leave remnants. Explicitly
saved screenshots, shell/terminal/tuios logs, OS traces and the site's own records
are outside this cleanup guarantee. The old global history file is not migrated
automatically into the new per-profile stores.

### `-stealth` and User-Agent

Without identity flags, Chromium's normal headless identity is left unchanged.
`-stealth` is an **opt-in, experimental compatibility mode**, not a guarantee of
passing anti-bot systems. It:

- derives UA from the actual running Chromium, changing only `HeadlessChrome/`
  to `Chrome/`, without inventing a browser version or OS;
- aligns the HTTP UA and `navigator.userAgent` through per-tab CDP configuration;
- disables the automation switch / Blink `AutomationControlled` marker, so the
  normal `navigator.webdriver` value is not `true`;
- preserves native UA Client Hints metadata when it is available, normalizing the
  headless brand. If an initial target cannot supply it, it omits those hints
  rather than fabricating hardware/platform details.

`-user-agent '...'` provides an explicit override and takes priority over the
derived UA. For arbitrary custom strings, Client Hints are omitted to avoid
attaching unrelated real-Chromium metadata. Prefer the matching-version derived
UA unless testing a specific compatibility issue.

This does not randomize fingerprints, spoof all graphics/plugins/devices, rotate
IPs, defeat challenges/captchas, or manufacture human behavior. The normal UI is
already driven by the user's clicks and typing; adding random delays does not
make automation genuinely human. Respect each site's access rules and limits.

## Native actions for golder and automation

`kitty-browse` exposes a versioned, owner-only JSON API on its optional Unix
socket. The implementation is compiled into this Go binary and talks over the
existing private CDP pipe: **runtime dependencies remain only kitty-browse and
Chromium**. It does not invoke terminal-browser, agent-browser, Node.js,
Playwright, Python or a remote-debugging TCP endpoint.

```bash
./bin/kitty-browse \
  -profile-dir "$PWD/.local/profiles/work" \
  -control-socket "$PWD/.local/live.sock" \
  https://example.com

./bin/kitty-browse info -socket "$PWD/.local/live.sock"
./bin/kitty-browse action -socket "$PWD/.local/live.sock" -- snapshot -i -c
./bin/kitty-browse action -socket "$PWD/.local/live.sock" -- click @e12
./bin/kitty-browse action -socket "$PWD/.local/live.sock" -- fill @e9 'hello'
./bin/kitty-browse action -socket "$PWD/.local/live.sock" -- get url
./bin/kitty-browse action -socket "$PWD/.local/live.sock" -- done
```

The CLI is a convenience client for the same `/v1/info` and `/v1/actions` API
that a future golder built-in tool should call directly. `/v1/info` returns
stable browser/tab/target IDs and machine-readable capability schemas; clients
must pin those IDs and re-discover after tab/navigation lifecycle changes.

Implemented native capabilities include AX snapshot/refs, CSS and accessible
locators, safe pointer/input/keyboard actions, waits, same-process and OOPIF
frames, dialogs, popup observation, tab/navigation/viewport lifecycle, bounded
network/console/error observation, response bodies, cookies/storage,
browser-scoped downloads, screenshots and PDF. Prefer snapshot/ref over visual
coordinate guessing. SSO, MFA, QR scan and consequential business writes remain
human-in-the-loop operations.

This is a stable typed API, not a claim of byte-for-byte agent-browser CLI
compatibility. Network interception/mock/HAR, video/trace, full device emulation,
closed-shadow piercing and WebMCP are not currently exposed. See
[`docs/action-api.md`](docs/action-api.md) for the full contract and
[`skill/kitty-browse/SKILL.md`](skill/kitty-browse/SKILL.md) for the golder
workflow.

## Controls

The top **two rows are mouse-clickable TUI controls**, not part of the webpage:

```text
[1 The Go Programming x][2 Example Domain x][+]
[<] [>] [R] https://go.dev/                         [Go] [Shot]
```

- Click a tab to switch without reloading; `x` closes it; `[+]` opens a blank tab.
- `[<]` / `[>]` use the current tab's actual Chromium history, including links
  clicked inside pages. Disabled controls are dimmed.
- `[R]` reloads. Click the address field, type a replacement URL, then click
  `[Go]` or press Enter. `[Shot]` saves a screenshot of the current viewport.
- Each tab retains its own page, scroll position and address-editor state.
  The last tab remains open; use `q` to quit the browser.
- These are styled terminal-cell buttons, not an embedded OS window. Page clicks
  and scrolling are sent to Chromium. Existing keyboard controls remain available.

| Key | Action |
|---|---|
| `Ctrl+L` / `l` | edit the address bar |
| `Enter` | navigate to the address |
| `Esc` | cancel editing / hint / picker |
| `Alt+Left` / `Left` | back |
| `Alt+Right` / `Right` | forward |
| `Ctrl+R` / `r` | reload |
| `q` / `Ctrl+C` / `Ctrl+Q` | quit |
| `Alt+F` | hint mode; type a label to click/focus |
| `H` | history picker (`j`/`k`, Enter) |
| `B` | bookmarks picker |
| `b` | toggle bookmark for the current page |
| `t` | toggle readable text mode |
| `s` / `S` | save viewport / full-page PNG screenshot |
| `T` / `W` | new tab / close active tab (outside address editing) |
| mouse click | left-click the page |
| mouse wheel | scroll the page |

Useful flags:

```text
-fps        maximum frames per second (default 24; supports fractional FPS)
-scroll-lines cell heights per wheel step (default 2; use 1 for gentler touchpad movement)
-quality    JPEG quality, 0-100 (default 85)
-width      maximum frame width in pixels
-height     maximum frame height in pixels
-load-timeout page load timeout (default 30s)
-measure-cells asynchronous pixel-size query (default true; use =false to disable)
-chrome     Chromium executable path (default: KITTY_BROWSE_CHROME, then auto-detect)
-control-socket optional local Unix socket for screenshots of this live browser
-screenshot-dir hotkey screenshot directory (default .local/screenshots)
-download-dir browser download directory (default: profile Downloads; temporary in private mode)
-profile-dir dedicated persistent profile (default: user config directory)
-private    isolated temporary profile and memory-only application history
-stealth    optional desktop-UA / reduced automation markers (experimental)
-user-agent explicit UA override (otherwise native or -stealth-derived)
```

## Screenshots of the live page

Screenshots come directly from Chromium, not the terminal cell grid. They
contain page pixels, but not the TUI address/help rows. In browse mode press
`s` for the current viewport or `S` for the full scrollable page. Files go to
`.local/screenshots/` with timestamped names; `-screenshot-dir` changes this.

For scripts or another pane, enable a local control socket at startup:

```bash
# In the browser pane (KITTY_BROWSE_CHROME configured as above):
./bin/kitty-browse -control-socket "$PWD/.local/live.sock" https://go.dev/

# From another shell, screenshot that SAME live tab:
./bin/kitty-browse screenshot -socket "$PWD/.local/live.sock" \
  -out .local/screenshots/viewport.png -json
./bin/kitty-browse screenshot -socket "$PWD/.local/live.sock" \
  -out .local/screenshots/full.png -full-page -json
```

`KITTY_BROWSE_SOCKET` supplies the socket path to either command. Use a separate
socket per browser instance; no guessing of which pane is active. The screenshot
command captures the selected tab; it never launches a fresh tab, reloads the URL, or changes viewport/scroll
position. Login and interactive state remain intact. Full-page capture includes
the current rendered document, not lazy content the site has not loaded yet.
Extremely large full-page captures are rejected at 100 million CSS pixels.

PNG files are private (`0600`) and existing files/symlinks are never overwritten.
The socket is also `0600`, offers only screenshots, never listens on TCP, and
is removed on a normal exit. If a browser crashes and leaves a stale socket,
use a new path or remove it only after confirming the old process has stopped.
On macOS keep the socket path short enough for the Unix socket path limit.

## Roadmap

- [x] Chromium lifecycle and page navigation
- [x] Kitty/iTerm2/Sixel frame streaming
- [x] Address bar and navigation keys
- [x] Mouse cell → viewport pixel mapping
- [x] Hint mode (`Alt+F`) driven by a page-side DOM query
- [x] History and bookmarks
- [x] Region rendering so a TUI can keep its own chrome
- [x] Idle PNG still frames, JPEG while the page is moving
- [x] Text extraction fallback for copy/readability
- [x] Optional `tuios` pane integration
- [x] Viewport/full-page screenshots of the live tab, via hotkeys or CLI
- [x] Clickable tab strip, address/navigation toolbar, and real Chromium tabs
- [x] Dedicated persistent profiles, private sessions and opt-in identity compatibility

## Design notes

`termcast` is a display transport, not a browser UI. This project's renderer
keeps tabs on row 1, navigation/address on row 2, and status on the last row:

1. it uses kernel-reported pixel dimensions or asynchronous `CSI 14t`/`16t`
   replies, which the normal input parser consumes as geometry, not keystrokes;
2. it switches to a lossless PNG once after a content change becomes idle;
3. it maps terminal mouse coordinates proportionally through the rendered
   viewport, including custom resolutions;
4. it places Kitty images into an explicit cell rectangle (`c/r`) from row 3
   to the row above status, so an 8x16 fallback on an 8x18 terminal cannot leave
   a black gap underneath; no webpage graphics overlap the two chrome rows.

Normal updates keep fixed Kitty image/placement IDs (`i=1,p=1`) and never clear
the body or delete the visible image before uploading its replacement. Identical
screencast frames are ACKed but dropped, and an unchanged idle page is not
periodically captured/redrawn. This avoids black flashes and JPEG/PNG cycling.

### Scrolling and latency

- Terminal input is bounded. Adjacent wheel reports are combined without
  crossing keyboard/click boundaries; reversed input replaces an unsent old
  direction tail. Horizontal touchpad drift is not treated as vertical input.
- A cancellable, serial worker sends accumulated wheel movement roughly every
  16ms, rather than blocking the UI on one CDP round trip per report. Changing
  tabs, navigating, resizing or clicking cancels pending old-target movement.
- CDP frames are ACKed independently of image decoding. Both raw and decoded
  frame mailboxes keep only the latest frame; stale frames are discarded before
  expensive decoding. Decoding is paced by `-fps`, and a final frame is retained.
- Ready frames are drawn immediately, without waiting for a second UI ticker.
  Scroll events do not redraw the toolbar. Idle PNG capture and title/history
  polling yield while input is busy.
- Opaque Kitty frames use lossless packed RGB with fast zlib compression
  (`f=24,o=z`), reused buffers, and one write per frame. Transparent images fall
  back to fast PNG. This avoids PNG re-encoding on the common scrolling path;
  the screenshot command still saves PNG files as before.

On the development Mac (Apple M1), the same 856x900 webpage screenshot measured
about **18.8ms / 45 writes / 180,798 wire bytes** with the previous PNG transport,
versus **5.3ms / 1 write / 149,101 wire bytes** with RGB+zlib. This is an encoding
benchmark, not an end-to-end display FPS guarantee. Terminal/tuios throughput,
viewport size and page complexity still limit the achieved frame rate.

To tune movement independently of frame rate:

```bash
./bin/kitty-browse -fps 30 -scroll-lines 1 https://go.dev/
```

SGR mouse reports provide quantized wheel steps, not macOS's original precise
touchpad pixel deltas. This improves response and removes queued work, but does
not make terminal scrolling identical to a native 60/120Hz browser.

For repeatable local encoding comparisons, first save a representative page PNG:

```bash
KITTY_BROWSE_BENCH_PNG="$PWD/.local/screenshots/viewport.png" \
  go test ./internal/render -run '^$' -bench BenchmarkKittyEncode -benchmem
```

## tuios integration

When `kitty-browse` runs inside a tuios pane (`TUIOS_ENV=1` plus a pane id and
socket), it renames the pane to the current page title. This is cosmetic and
best-effort: outside tuios, or when the `tuios` binary is unavailable, it is a
no-op. Exiting restores the shell-derived pane name.

Kitty frames use a first packet containing image data and explicit pixel
dimensions. This avoids a rasterm v0.1.17 / tuios compatibility issue:
an empty PNG first packet with no dimensions can upload the image but leave
it with a zero-cell placement, making the page invisible.

For startup, frame and draw diagnostics:

```bash
KITTY_BROWSE_DEBUG="$PWD/.local/render.log" go run ./cmd/kitty-browse \
  "file://$PWD/internal/render/testdata/page.html"
```

Distinguish the layers when debugging: `first screencast frame` means Chromium
produced pixels; `still frame` means PNG capture succeeded. Once-per-second
`perf` lines report input coalescing, pending/in-flight scroll movement, CDP
calls, frames received/ACKed/decoded/dropped, terminal writes (`Drawn`) and
`WireBytes`. `DrawLast`/`DrawTotal` include encoding and writing to the PTY;
they do **not** measure when the host terminal actually displayed the pixels.
`tuios capture-pane` and `tuios screenshot` capture character cells, not Kitty
image pixels; a blank body in those captures is not evidence of missing frames.
Check the visible pane for the blue test page and orange swatch.

## References

- [`chromedp/termcast`](https://github.com/chromedp/termcast) — MIT
- [`chromedp/chromedp`](https://github.com/chromedp/chromedp) — MIT
- [`sanohiro/casty`](https://github.com/sanohiro/casty) — MIT; input/hint/address
  bar reference implementation

## License

MIT. See [LICENSE](LICENSE).
