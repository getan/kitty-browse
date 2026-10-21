# kitty-browse

A small Go terminal browser: real Chromium pixels in Kitty/iTerm2/Sixel
terminals, without the Electron/Rust stack.

> Status: early scaffold. It loads a page and streams it to the terminal; the
> TUI, mouse mapping, hint mode, history and tabs are the next layers.

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
| `Esc` | cancel editing, or quit while browsing |
| `Alt+Left` / `Left` | back |
| `Alt+Right` / `Right` | forward |
| `Ctrl+R` / `r` | reload |
| `q` / `Ctrl+C` / `Ctrl+Q` | quit |
| mouse click | left-click the page |
| mouse wheel | scroll the page |

Useful flags:

```text
-fps        maximum frames per second (default 8)
-quality    JPEG quality, 0-100 (default 85)
-width      maximum frame width in pixels
-height     maximum frame height in pixels
-load-timeout page load timeout (default 30s)
```

## Roadmap

- [x] Chromium lifecycle and page navigation
- [x] Kitty/iTerm2/Sixel frame streaming
- [x] Address bar and navigation keys
- [x] Mouse cell → viewport pixel mapping
- [ ] Hint mode (`Alt+F`) driven by a page-side DOM query
- [ ] History and bookmarks
- [ ] Region rendering so a TUI can keep its own chrome
- [ ] Text extraction fallback for copy/readability
- [ ] Optional `tuios` pane integration

## Design notes

`termcast` is a display transport, not a browser UI. It clears the terminal for
each new frame and sends JPEG frames at a bounded rate. A production TUI should
fork or wrap it to:

1. render into a reserved terminal region;
2. use the terminal's real pixel size (`CSI 14t`) instead of the default
   1024×768 cap;
3. switch to a high-quality still frame when the page is idle;
4. map terminal mouse coordinates through device scale factor and browser zoom.

## References

- [`chromedp/termcast`](https://github.com/chromedp/termcast) — MIT
- [`chromedp/chromedp`](https://github.com/chromedp/chromedp) — MIT
- [`sanohiro/casty`](https://github.com/sanohiro/casty) — MIT; input/hint/address
  bar reference implementation

## License

MIT. See [LICENSE](LICENSE).
