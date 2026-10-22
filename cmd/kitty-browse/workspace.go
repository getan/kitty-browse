package main

import (
	"context"
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
	model.Tabs = make([]ui.TabInfo, len(sessions))
	for i, sess := range sessions {
		model.Tabs[i] = ui.TabInfo{Title: titles[sess], URL: sess.URL()}
	}
	model.ActiveTab = tabs.Index()
	if active := tabs.Active(); active != nil {
		model.CanGoBack, model.CanGoForward = active.CanBack(), active.CanForward()
	}
}
