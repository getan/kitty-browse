package browser

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/getan/kitty-browse/internal/captureguard"
)

const maxScreenshotPixels = 100_000_000

// Screenshot captures the current viewport, or the complete scrollable page.
//
// The browser remains bound to the Session context. The supplied context is
// used only to cancel this operation; its values are deliberately not passed
// to chromedp. Every capture also has a ten-second upper bound.
func (s *Session) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("capture screenshot: %w", err)
	}

	captureCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	endCapture := captureguard.Begin(s.TargetID())
	stop := context.AfterFunc(ctx, cancel)
	defer func() {
		endCapture()
		stop()
		cancel()
	}()

	params := page.CaptureScreenshotParams{
		Format:                page.CaptureScreenshotFormatPng,
		FromSurface:           boolPtr(true),
		CaptureBeyondViewport: boolPtr(false),
	}
	if fullPage {
		metrics, err := chromedp.Call(captureCtx, page.GetLayoutMetrics, cdp.Empty{})
		if err != nil {
			return nil, fmt.Errorf("measure page for screenshot: %w", err)
		}
		size := metrics.CSSContentSize
		if size == nil || !validScreenshotDimension(size.Width) || !validScreenshotDimension(size.Height) {
			return nil, fmt.Errorf("capture full-page screenshot: page has invalid dimensions")
		}
		if size.Width > maxScreenshotPixels/size.Height {
			return nil, fmt.Errorf("capture full-page screenshot: page is too large (%.0fx%.0f; limit is %d pixels)", size.Width, size.Height, maxScreenshotPixels)
		}
		params.Clip = &page.Viewport{X: size.X, Y: size.Y, Width: size.Width, Height: size.Height, Scale: 1}
		params.CaptureBeyondViewport = boolPtr(true)
	}

	result, err := chromedp.Call(captureCtx, page.CaptureScreenshot, params)
	if err != nil {
		return nil, fmt.Errorf("capture screenshot: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("capture screenshot: browser returned an empty image")
	}
	return result.Data, nil
}

func boolPtr(value bool) *bool { return &value }

func validScreenshotDimension(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
