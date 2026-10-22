package main

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"time"

	"github.com/getan/kitty-browse/internal/browser"
	"github.com/getan/kitty-browse/internal/render"
	"github.com/getan/kitty-browse/internal/term"
	"github.com/getan/kitty-browse/internal/ui"
)

func paneGeometry(rows, cols int, pixels term.CellPixels, width, height int) term.Geometry {
	g := term.GeometryFor(rows, cols, pixels)
	if width > 0 {
		g.ContentWidthPx = width
	}
	if height > 0 {
		g.ContentHeightPx = height
	}
	g.ContentWidthPx = max(g.ContentWidthPx, 1)
	g.ContentHeightPx = max(g.ContentHeightPx, 1)
	return g
}

func debugURL(raw string) string {
	if raw == "" {
		return ""
	}
	normalized, err := browser.Normalize(raw)
	if err != nil {
		return "[invalid-url]"
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "[invalid-url]"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

func mergeCellPixels(current, reply term.CellPixels, rows, cols int) term.CellPixels {
	if rows <= 0 || cols <= 0 {
		return current
	}
	if reply.CellW > 0 && reply.CellH > 0 {
		current.CellW, current.CellH = reply.CellW, reply.CellH
		current.WidthPx, current.HeightPx = cols*reply.CellW, rows*reply.CellH
	}
	if reply.WidthPx > 0 && reply.HeightPx > 0 {
		current.WidthPx, current.HeightPx = reply.WidthPx, reply.HeightPx
		current.CellW = max(reply.WidthPx/cols, 1)
		current.CellH = max(reply.HeightPx/rows, 1)
	}
	return current
}

func startTabStream(sess *browser.Session, g term.Geometry, fps float64, quality int) (*render.Stream, error) {
	return render.Start(sess.Context(), render.Config{
		FPS: fps, Quality: quality, Width: g.ContentWidthPx, Height: g.ContentHeightPx,
		Cols: g.Cols, Rows: g.ContentRows,
	})
}

func cacheTabTitle(titles map[*browser.Session]string, sess *browser.Session) {
	ctx, cancel := context.WithTimeout(sess.Context(), time.Second)
	defer cancel()
	if title, err := sess.Title(ctx); err == nil {
		titles[sess] = title
	}
}

func syncTabChrome(model *ui.Model, tabs *browser.Tabs, titles map[*browser.Session]string) {
	sessions := tabs.Sessions()
	live := make(map[*browser.Session]bool, len(sessions))
	model.Tabs = make([]ui.TabInfo, len(sessions))
	for i, sess := range sessions {
		live[sess] = true
		model.Tabs[i] = ui.TabInfo{Title: titles[sess], URL: sess.URL()}
	}
	for sess := range titles {
		if !live[sess] {
			delete(titles, sess)
		}
	}
	model.ActiveTab = tabs.Index()
	if active := tabs.Active(); active != nil {
		model.CanGoBack, model.CanGoForward = active.CanBack(), active.CanForward()
	}
}

func frameInterval(fps float64) (time.Duration, error) {
	if math.IsNaN(fps) || math.IsInf(fps, 0) || fps < 0.1 || fps > 240 {
		return 0, fmt.Errorf("fps must be finite and between 0.1 and 240")
	}
	return time.Duration(float64(time.Second) / fps), nil
}

func validScrollLines(lines float64) bool {
	return !math.IsNaN(lines) && !math.IsInf(lines, 0) && lines >= 0.1 && lines <= 100
}

func scrollPixels(g term.Geometry, steps int, lines float64) float64 {
	return float64(steps) * float64(g.ContentHeightPx) / float64(max(g.ContentRows, 1)) * lines
}

func interruptsScroll(kind ui.ActionKind) bool {
	switch kind {
	case ui.ActionNavigate, ui.ActionBack, ui.ActionForward, ui.ActionReload,
		ui.ActionNewTab, ui.ActionSelectTab, ui.ActionCloseTab, ui.ActionOverlaySelect,
		ui.ActionToggleText, ui.ActionShowHints, ui.ActionHint,
		ui.ActionScreenshot, ui.ActionScreenshotFull, ui.ActionQuit:
		return true
	}
	return false
}
