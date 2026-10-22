package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

const (
	maxObservedDownloads = 64
	recentDownloadWindow = 5 * time.Second
)

// Download is a metadata-only, browser-scoped download record. File contents
// are never read by this API. Path is present only for a completed download
// whose GUID-named file is safely inside the configured download directory.
type Download struct {
	GUID              string  `json:"guid"`
	URL               string  `json:"url,omitempty"`
	SuggestedFilename string  `json:"suggestedFilename,omitempty"`
	State             string  `json:"state"`
	ReceivedBytes     float64 `json:"receivedBytes"`
	TotalBytes        float64 `json:"totalBytes"`
	Path              string  `json:"path,omitempty"`
	Scope             string  `json:"scope"`

	beganAt  time.Time
	consumed bool
	claimed  bool
}

type downloadContextKey struct{}

type downloadConfig struct {
	dir string

	initMu      sync.Mutex
	initialized bool
	initErr     error

	mu        sync.Mutex
	downloads []*Download
	byGUID    map[string]*Download
	changed   chan struct{}
}

func newDownloadConfig(dir string) *downloadConfig {
	return &downloadConfig{dir: dir, byGUID: make(map[string]*Download), changed: make(chan struct{})}
}

func withDownloadConfig(ctx context.Context, config *downloadConfig) context.Context {
	return context.WithValue(ctx, downloadContextKey{}, config)
}

func downloadConfigFrom(ctx context.Context) *downloadConfig {
	config, _ := ctx.Value(downloadContextKey{}).(*downloadConfig)
	return config
}

// startDownloadObservation installs one browser-level listener and download
// policy for all tabs descended from the allocator context.
func startDownloadObservation(ctx context.Context) error {
	config := downloadConfigFrom(ctx)
	if config == nil {
		return nil
	}
	config.initMu.Lock()
	defer config.initMu.Unlock()
	if config.initialized {
		return config.initErr
	}
	config.initialized = true

	beginEvents := chromedp.BrowserEvents(ctx, cdpbrowser.DownloadWillBegin)
	progressEvents := chromedp.BrowserEvents(ctx, cdpbrowser.DownloadProgress)
	enabled := true
	_, err := chromedp.CallBrowser(ctx, cdpbrowser.SetDownloadBehavior, cdpbrowser.SetDownloadBehaviorParams{
		Behavior:      cdpbrowser.SetDownloadBehaviorBehaviorAllowAndName,
		DownloadPath:  config.dir,
		EventsEnabled: &enabled,
	})
	if err != nil {
		config.initErr = fmt.Errorf("enable browser downloads: %w", err)
		return config.initErr
	}
	go func() {
		for event, err := range beginEvents {
			if err != nil {
				return
			}
			config.downloadWillBegin(event)
		}
	}()
	go func() {
		for event, err := range progressEvents {
			if err != nil {
				return
			}
			config.downloadProgress(event)
		}
	}()
	return nil
}

func (c *downloadConfig) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *downloadConfig) recordLocked(guid string) *Download {
	if item := c.byGUID[guid]; item != nil {
		return item
	}
	item := &Download{GUID: guid, State: "inProgress", Scope: "browser", beganAt: time.Now()}
	c.downloads = append(c.downloads, item)
	c.byGUID[guid] = item
	for len(c.downloads) > maxObservedDownloads {
		old := c.downloads[0]
		c.downloads[0] = nil
		c.downloads = c.downloads[1:]
		if c.byGUID[old.GUID] == old {
			delete(c.byGUID, old.GUID)
		}
	}
	return item
}

func (c *downloadConfig) downloadWillBegin(event cdpbrowser.EventDownloadWillBegin) {
	c.mu.Lock()
	item := c.recordLocked(event.GUID)
	item.URL = sanitizeObservedURL(event.URL)
	item.SuggestedFilename = event.SuggestedFilename
	c.signalLocked()
	c.mu.Unlock()
}

func (c *downloadConfig) downloadProgress(event cdpbrowser.EventDownloadProgress) {
	c.mu.Lock()
	item := c.recordLocked(event.GUID)
	item.State = string(event.State)
	item.ReceivedBytes = event.ReceivedBytes
	item.TotalBytes = event.TotalBytes
	if event.State == cdpbrowser.DownloadProgressStateCompleted {
		if path, err := c.safePath(event.GUID, event.FilePath); err == nil {
			item.Path = path
		} else {
			item.State = "failed"
		}
	}
	c.signalLocked()
	c.mu.Unlock()
}

func (c *downloadConfig) safePath(guid, reported string) (string, error) {
	if guid == "" || filepath.Base(guid) != guid || guid == "." || guid == ".." {
		return "", fmt.Errorf("unsafe download GUID")
	}
	expected := filepath.Join(c.dir, guid)
	path := expected
	if reported != "" {
		var err error
		reported, err = filepath.Abs(reported)
		if err != nil {
			return "", err
		}
		reported = filepath.Clean(reported)
		if reported != expected {
			// Chromium may canonicalize a platform path (for example /var to
			// /private/var on macOS). Accept that spelling only when it names the
			// exact same file, and still return our configured-directory spelling.
			expectedInfo, expectedErr := os.Stat(expected)
			reportedInfo, reportedErr := os.Stat(reported)
			if expectedErr != nil || reportedErr != nil || !os.SameFile(expectedInfo, reportedInfo) {
				return "", fmt.Errorf("download path is outside the configured directory")
			}
		}
	}
	rel, err := filepath.Rel(c.dir, path)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || (len(rel) > 3 && rel[:3] == ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("download path is outside the configured directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("download path is not a regular file")
	}
	return path, nil
}

func cloneDownload(item *Download) Download {
	return Download{GUID: item.GUID, URL: item.URL, SuggestedFilename: item.SuggestedFilename,
		State: item.State, ReceivedBytes: item.ReceivedBytes, TotalBytes: item.TotalBytes,
		Path: item.Path, Scope: item.Scope}
}

// ListDownloads returns up to the latest 64 browser-scoped records without
// consuming them.
func (s *Session) ListDownloads() ([]Download, error) {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return nil, automationError("target_closed", "browser target is closed")
	}
	config := downloadConfigFrom(s.ctx)
	if config == nil {
		return nil, automationError("downloads_unavailable", "downloads are not configured")
	}
	config.mu.Lock()
	defer config.mu.Unlock()
	result := make([]Download, 0, len(config.downloads))
	for _, item := range config.downloads {
		result = append(result, cloneDownload(item))
	}
	return result, nil
}

// WaitDownload waits for a GUID, or for the newest unconsumed download begun
// in the preceding five seconds. Download observations are browser-scoped: a
// wait issued through any tab may match a download initiated by any tab.
func (s *Session) WaitDownload(ctx context.Context, guid string) (Download, error) {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil {
		return Download{}, automationError("target_closed", "browser target is closed")
	}
	config := downloadConfigFrom(s.ctx)
	if config == nil {
		return Download{}, automationError("downloads_unavailable", "downloads are not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var selected *Download
	defer func() {
		if selected != nil {
			config.mu.Lock()
			selected.claimed = false
			config.signalLocked()
			config.mu.Unlock()
		}
	}()
	for {
		config.mu.Lock()
		if selected == nil {
			if guid != "" {
				if candidate := config.byGUID[guid]; candidate != nil && !candidate.consumed && !candidate.claimed {
					selected, candidate.claimed = candidate, true
				}
			} else {
				cutoff := time.Now().Add(-recentDownloadWindow)
				for i := len(config.downloads) - 1; i >= 0; i-- {
					candidate := config.downloads[i]
					if !candidate.beganAt.Before(cutoff) && !candidate.consumed && !candidate.claimed {
						selected, candidate.claimed = candidate, true
						break
					}
				}
			}
		}
		if selected != nil && selected.State != "inProgress" {
			selected.consumed = true
			result := cloneDownload(selected)
			config.mu.Unlock()
			switch result.State {
			case string(cdpbrowser.DownloadProgressStateCompleted):
				if result.Path == "" {
					return Download{}, automationError("download_failed", "download completed without a safe file path")
				}
				return result, nil
			case string(cdpbrowser.DownloadProgressStateCanceled):
				return Download{}, automationError("download_canceled", "download was canceled")
			default:
				return Download{}, automationError("download_failed", "download failed")
			}
		}
		changed := config.changed
		config.mu.Unlock()
		select {
		case <-ctx.Done():
			return Download{}, ctx.Err()
		case <-s.ctx.Done():
			return Download{}, automationError("target_closed", "browser target is closed")
		case <-changed:
		}
	}
}
