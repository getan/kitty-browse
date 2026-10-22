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
// allocator context. An empty UserAgent deliberately leaves Chromium's real
// user agent unchanged.
type LaunchOptions struct {
	ExecutablePath string
	ProfileDir     string
	UserAgent      string
	Private        bool
	Stealth        bool
}

// ProfileInfo describes the kitty-browse-owned data associated with a browser
// context. Private contexts do not have persistent profile or state paths.
type ProfileInfo struct {
	Dir       string
	Private   bool
	StatePath string
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
		ctx, allocatorCancel := chromedp.NewExecAllocator(parent, allocatorOptions...)
		var once sync.Once
		cancel := func() { once.Do(allocatorCancel) }
		return withIdentityOptions(ctx, identity), cancel, ProfileInfo{Private: true}, nil
	}

	profileDir, err := resolveProfileDir(opts.ProfileDir)
	if err != nil {
		return nil, nil, empty, err
	}
	if err := prepareProfileDir(profileDir); err != nil {
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
		Dir:       profileDir,
		StatePath: filepath.Join(profileDir, profileStateFile),
	}
	return withIdentityOptions(ctx, identity), cancel, info, nil
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
