# kitty-browse

A small Go terminal browser: real Chromium pixels in Kitty/iTerm2/Sixel
terminals, without the Electron/Rust stack.

> Status: functional prototype. Address bar, mouse, hints, history/bookmarks,
> text fallback and optional tuios title reporting are implemented; tabs and
> richer page controls are future work.

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
  row 1 free for the address bar.
- The browser lifecycle stays separate so a future `tuios` pane or automation
  tool can reuse the same session.

## Requirements

- Go 1.25+
- Chrome or `chrome-headless-shell`
- A terminal with graphics support: Kitty, Ghostty, WezTerm, iTerm2, or a
  Sixel-capable terminal
- Inside tmux, enable passthrough so the graphics escapes reach the terminal

## Run

```bash
go run ./cmd/kitty-browse https://example.com
go run ./cmd/kitty-browse -fps 12 -quality 90 example.com
```

## Controls

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
| mouse click | left-click the page |
| mouse wheel | scroll the page |

Useful flags:

```text
-fps        maximum frames per second (default 8)
-quality    JPEG quality, 0-100 (default 85)
-width      maximum frame width in pixels
-height     maximum frame height in pixels
-load-timeout page load timeout (default 30s)
-measure-cells query CSI 14t/16t for exact cell pixels (may leave terminal replies)
```

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

## Design notes

`termcast` is a display transport, not a browser UI. This project's renderer
keeps the address bar on row 1 and the help/status line on the last row:

1. it uses the terminal's real pixel size (`CSI 14t`/`16t`) when available;
2. it switches to a lossless PNG still frame when the page is idle;
3. it maps terminal mouse coordinates through the measured cell size;
4. it redraws only the page region, leaving both chrome rows intact.

## tuios integration

When `kitty-browse` runs inside a tuios pane (`TUIOS_ENV=1` plus a pane id and
socket), it renames the pane to the current page title. This is cosmetic and
best-effort: outside tuios, or when the `tuios` binary is unavailable, it is a
no-op. Exiting restores the shell-derived pane name.

## References

- [`chromedp/termcast`](https://github.com/chromedp/termcast) — MIT
- [`chromedp/chromedp`](https://github.com/chromedp/chromedp) — MIT
- [`sanohiro/casty`](https://github.com/sanohiro/casty) — MIT; input/hint/address
  bar reference implementation

## License

MIT. See [LICENSE](LICENSE).
