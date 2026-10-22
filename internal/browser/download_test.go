package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
)

func TestDownloadDirectoryLifecycle(t *testing.T) {
	t.Run("private default is removed", func(t *testing.T) {
		_, cancel, info, err := NewBrowserContext(context.Background(), LaunchOptions{Private: true})
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(info.DownloadDir) {
			t.Fatalf("download dir is not absolute: %q", info.DownloadDir)
		}
		if stat, err := os.Stat(info.DownloadDir); err != nil || stat.Mode().Perm() != 0o700 {
			t.Fatalf("private download directory: stat=%v err=%v", stat, err)
		}
		cancel()
		if _, err := os.Stat(info.DownloadDir); !os.IsNotExist(err) {
			t.Fatalf("private downloads remain after cancel: %v", err)
		}
	})

	t.Run("explicit private artifact remains", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "artifacts")
		_, cancel, info, err := NewBrowserContext(context.Background(), LaunchOptions{Private: true, DownloadDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if _, err := os.Stat(info.DownloadDir); err != nil {
			t.Fatalf("explicit artifact directory was removed: %v", err)
		}
	})

	t.Run("persistent defaults under profile", func(t *testing.T) {
		requireUnixProfileLock(t)
		profile := filepath.Join(t.TempDir(), "profile")
		_, cancel, info, err := NewBrowserContext(context.Background(), LaunchOptions{ProfileDir: profile})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if info.DownloadDir != filepath.Join(profile, "Downloads") {
			t.Fatalf("DownloadDir = %q", info.DownloadDir)
		}
		if _, err := os.Stat(info.DownloadDir); err != nil {
			t.Fatalf("persistent downloads removed: %v", err)
		}
	})
}

func TestDownloadDirectoryRejectsFileAndSymlink(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := NewBrowserContext(context.Background(), LaunchOptions{Private: true, DownloadDir: file}); err == nil {
		t.Fatal("regular file accepted as download directory")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, _, err := NewBrowserContext(context.Background(), LaunchOptions{Private: true, DownloadDir: link}); err == nil {
		t.Fatal("symlink accepted as download directory")
	}
}

func TestDownloadObservationWaitListAndBounds(t *testing.T) {
	dir := t.TempDir()
	config := newDownloadConfig(dir)
	sess := &Session{ctx: withDownloadConfig(context.Background(), config)}
	config.downloadWillBegin(cdpbrowser.EventDownloadWillBegin{
		GUID: "one", URL: "https://example.test/file?token=secret&kind=public", SuggestedFilename: "report.txt",
	})
	if err := os.WriteFile(filepath.Join(dir, "one"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.downloadProgress(cdpbrowser.EventDownloadProgress{
		GUID: "one", State: cdpbrowser.DownloadProgressStateCompleted, ReceivedBytes: 7, TotalBytes: 7,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := sess.WaitDownload(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.GUID != "one" || got.Path != filepath.Join(dir, "one") || got.Scope != "browser" || got.State != "completed" {
		t.Fatalf("download = %+v", got)
	}
	if !strings.Contains(got.URL, "%5BREDACTED%5D") {
		t.Fatalf("download URL was not redacted: %q", got.URL)
	}
	list, err := sess.ListDownloads()
	if err != nil || len(list) != 1 || list[0].SuggestedFilename != "report.txt" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	for i := 0; i < maxObservedDownloads+5; i++ {
		config.downloadWillBegin(cdpbrowser.EventDownloadWillBegin{GUID: "extra-" + strings.Repeat("x", i+1)})
	}
	list, err = sess.ListDownloads()
	if err != nil || len(list) != maxObservedDownloads {
		t.Fatalf("bounded list length = %d, err=%v", len(list), err)
	}
}

func TestDownloadFailureAndCancelAreStructured(t *testing.T) {
	for _, state := range []cdpbrowser.DownloadProgressState{
		cdpbrowser.DownloadProgressStateCanceled,
		cdpbrowser.DownloadProgressState("failed"),
	} {
		config := newDownloadConfig(t.TempDir())
		sess := &Session{ctx: withDownloadConfig(context.Background(), config)}
		config.downloadProgress(cdpbrowser.EventDownloadProgress{GUID: string(state), State: state})
		_, err := sess.WaitDownload(context.Background(), string(state))
		automationErr, ok := err.(*AutomationError)
		if !ok || (automationErr.Code != "download_canceled" && automationErr.Code != "download_failed") {
			t.Fatalf("state %q error = %#v", state, err)
		}
	}
}
