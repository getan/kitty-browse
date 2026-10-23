package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/chromedp/chromedp"
)

const (
	profileStateFile = "kitty-browse-state.json"
	profileLockFile  = ".kitty-browse.lock"
	maxUserAgentLen  = 4096
)

// LaunchOptions controls the Chromium process created from the returned
// allocator context. Stealth is on by default in kitty-browse, so the process UA
// is the browser's real one with only the headless token removed. An empty
// UserAgent lets that normalization happen; a non-empty one takes precedence.
type LaunchOptions struct {
	ExecutablePath string
	ProfileDir     string
	DownloadDir    string
	UserAgent      string
	Private        bool
	Stealth        bool
}

// ProfileInfo describes the kitty-browse-owned data associated with a browser
// context. Private contexts do not have persistent profile or state paths.
type ProfileInfo struct {
	Dir         string
	DownloadDir string
	Private     bool
	StatePath   string
}

// DefaultProfileDir returns kitty-browse's default, independent Chromium
// profile. It never points at or copies a user's regular Chrome profile.
func DefaultProfileDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find user config directory: %w", err)
		}
	}
	if base == "" {
		return "", fmt.Errorf("user config directory is empty")
	}
	abs, err := filepath.Abs(filepath.Join(base, "kitty-browse", "profiles", "default"))
	if err != nil {
		return "", fmt.Errorf("resolve default profile directory: %w", err)
	}
	return abs, nil
}

// NewBrowserContext creates a chromedp exec-allocator context. Persistent
// contexts use a kitty-browse profile protected by a non-blocking process lock;
// private contexts leave user-data-dir unset so chromedp creates and removes a
// temporary directory when Chromium actually starts.
func NewBrowserContext(parent context.Context, opts LaunchOptions) (context.Context, context.CancelFunc, ProfileInfo, error) {
	var empty ProfileInfo
	if opts.Private && opts.ProfileDir != "" {
		return nil, nil, empty, fmt.Errorf("private browsing cannot use a persistent profile directory")
	}
	if err := validateUserAgent(opts.UserAgent); err != nil {
		return nil, nil, empty, err
	}

	// Clone the defaults rather than appending to their backing array. This
	// prevents one launch's options from mutating chromedp's package defaults or
	// leaking into a later launch.
	allocatorOptions := make([]chromedp.ExecAllocatorOption, len(chromedp.DefaultExecAllocatorOptions))
	copy(allocatorOptions, chromedp.DefaultExecAllocatorOptions[:])
	if opts.ExecutablePath != "" {
		allocatorOptions = append(allocatorOptions, chromedp.ExecPath(opts.ExecutablePath))
	}
	if opts.Stealth {
		allocatorOptions = append(allocatorOptions,
			chromedp.Flag("enable-automation", false),
			chromedp.Flag("disable-blink-features", "AutomationControlled"),
		)
	}
	// Per-target CDP overrides keep HTTP and navigator UA aligned. Do not set
	// the process UA flag: Browser.getVersion must retain the native identity.
	identity := IdentityOptions{Stealth: opts.Stealth, UserAgent: opts.UserAgent}

	if opts.Private {
		downloadDir, temporary, err := prepareDownloadDir(opts.DownloadDir, "")
		if err != nil {
			return nil, nil, empty, err
		}
		ctx, allocatorCancel := chromedp.NewExecAllocator(parent, allocatorOptions...)
		var once sync.Once
		cancel := func() {
			once.Do(func() {
				allocatorCancel()
				if temporary {
					_ = os.RemoveAll(downloadDir)
				}
			})
		}
		ctx = withIdentityOptions(ctx, identity)
		ctx = withDownloadConfig(ctx, newDownloadConfig(downloadDir))
		return ctx, cancel, ProfileInfo{DownloadDir: downloadDir, Private: true}, nil
	}

	profileDir, err := resolveProfileDir(opts.ProfileDir)
	if err != nil {
		return nil, nil, empty, err
	}
	if err := prepareProfileDir(profileDir); err != nil {
		return nil, nil, empty, err
	}
	// A profile is persistent so cookies and logins survive, not so the last
	// window's tabs come back: the browser shows what this process opened. Left
	// alone, a profile from a process that was killed restores its old tabs and
	// activates one of them, pushing the requested page into the background.
	if err := discardRestoredSession(profileDir); err != nil {
		return nil, nil, empty, err
	}
	downloadDir, _, err := prepareDownloadDir(opts.DownloadDir, profileDir)
	if err != nil {
		return nil, nil, empty, err
	}
	lock, err := acquireProfileLock(filepath.Join(profileDir, profileLockFile))
	if err != nil {
		return nil, nil, empty, fmt.Errorf("lock profile %q: %w", profileDir, err)
	}

	allocatorOptions = append(allocatorOptions, chromedp.UserDataDir(profileDir))
	ctx, allocatorCancel := chromedp.NewExecAllocator(parent, allocatorOptions...)
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			// NewExecAllocator's cancel waits for all allocated browsers. Keep the
			// advisory lock until that wait has completed.
			allocatorCancel()
			_ = releaseProfileLock(lock)
		})
	}
	info := ProfileInfo{
		Dir:         profileDir,
		DownloadDir: downloadDir,
		StatePath:   filepath.Join(profileDir, profileStateFile),
	}
	ctx = withIdentityOptions(ctx, identity)
	ctx = withDownloadConfig(ctx, newDownloadConfig(downloadDir))
	return ctx, cancel, info, nil
}

// prepareDownloadDir creates an owner-only download directory. Private
// contexts use a temporary directory unless the caller explicitly chooses an
// artifact directory; persistent contexts default to <profile>/Downloads.
func prepareDownloadDir(configured, profileDir string) (dir string, temporary bool, err error) {
	if strings.TrimSpace(configured) == "" {
		if profileDir == "" {
			dir, err = os.MkdirTemp("", "kitty-browse-downloads-")
			if err != nil {
				return "", false, fmt.Errorf("create private download directory: %w", err)
			}
			temporary = true
		} else {
			dir = filepath.Join(profileDir, "Downloads")
		}
	} else {
		dir, err = filepath.Abs(configured)
		if err != nil {
			return "", false, fmt.Errorf("resolve download directory %q: %w", configured, err)
		}
		dir = filepath.Clean(dir)
	}
	if info, statErr := os.Lstat(dir); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("download path %q is a symbolic link", dir)
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("download path %q is not a directory", dir)
		}
	} else if !os.IsNotExist(statErr) {
		return "", false, fmt.Errorf("inspect download directory %q: %w", dir, statErr)
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("create download directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		if temporary {
			_ = os.RemoveAll(dir)
		}
		return "", false, fmt.Errorf("secure download directory %q: %w", dir, err)
	}
	return dir, temporary, nil
}

func validateUserAgent(userAgent string) error {
	if len(userAgent) > maxUserAgentLen {
		return fmt.Errorf("user agent is too long (maximum %d bytes)", maxUserAgentLen)
	}
	if !utf8.ValidString(userAgent) {
		return fmt.Errorf("user agent is not valid UTF-8")
	}
	for _, r := range userAgent {
		if unicode.IsControl(r) {
			return fmt.Errorf("user agent contains a control character")
		}
	}
	return nil
}

func resolveProfileDir(configured string) (string, error) {
	if configured == "" {
		return DefaultProfileDir()
	}
	abs, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("resolve profile directory %q: %w", configured, err)
	}
	return filepath.Clean(abs), nil
}

func prepareProfileDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("profile directory is empty")
	}
	info, err := os.Stat(dir)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("profile path %q is not a directory", dir)
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("inspect profile directory %q: %w", dir, err)
	case os.IsNotExist(err):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create profile directory %q: %w", dir, err)
		}
	}
	// Only secure the profile itself; do not change permissions on any existing
	// parent selected by the user.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure profile directory %q: %w", dir, err)
	}
	return nil
}

// discardRestoredSession removes the files Chromium uses to reopen a previous
// window. It touches nothing else in the profile: cookies, storage and history
// stay.
func discardRestoredSession(profileDir string) error {
	profile := filepath.Join(profileDir, "Default")
	targets := []string{
		filepath.Join(profile, "Sessions"),
		filepath.Join(profile, "Current Session"),
		filepath.Join(profile, "Current Tabs"),
		filepath.Join(profile, "Last Session"),
		filepath.Join(profile, "Last Tabs"),
	}
	for _, path := range targets {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("discard restored session %q: %w", path, err)
		}
	}
	return nil
}
