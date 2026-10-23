package browser

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultProfileDirUsesXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	got, err := DefaultProfileDir()
	if err != nil {
		t.Fatalf("DefaultProfileDir: %v", err)
	}
	want := filepath.Join(root, "kitty-browse", "profiles", "default")
	if got != want {
		t.Fatalf("DefaultProfileDir = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("DefaultProfileDir created its result; Stat error = %v", err)
	}
}

func TestDefaultProfileDirFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("os.UserConfigDir is unavailable: %v", err)
	}

	got, err := DefaultProfileDir()
	if err != nil {
		t.Fatalf("DefaultProfileDir: %v", err)
	}
	want, err := filepath.Abs(filepath.Join(base, "kitty-browse", "profiles", "default"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DefaultProfileDir = %q, want UserConfigDir-based %q", got, want)
	}
}

func TestNewBrowserContextCustomProfile(t *testing.T) {
	requireUnixProfileLock(t)
	root := t.TempDir()
	parent := filepath.Join(root, "existing")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(mustGetwd(t), filepath.Join(parent, "profile"))
	if err != nil {
		t.Fatal(err)
	}

	_, cancel, info, err := NewBrowserContext(context.Background(), LaunchOptions{ProfileDir: relative})
	if err != nil {
		t.Fatalf("NewBrowserContext: %v", err)
	}
	defer cancel()
	wantDir, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	if info.Private || info.Dir != wantDir {
		t.Fatalf("ProfileInfo = %+v, want persistent dir %q", info, wantDir)
	}
	if info.StatePath != filepath.Join(wantDir, "kitty-browse-state.json") {
		t.Fatalf("StatePath = %q", info.StatePath)
	}
	stat, err := os.Stat(wantDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := stat.Mode().Perm(); got != 0o700 {
		t.Fatalf("profile permissions = %04o, want 0700", got)
	}
	parentStat, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got := parentStat.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing parent permissions changed to %04o", got)
	}
}

func TestProfileLockRejectsConcurrentUseAndReopensAfterCancel(t *testing.T) {
	requireUnixProfileLock(t)
	profile := filepath.Join(t.TempDir(), "profile")

	_, cancelFirst, _, err := NewBrowserContext(context.Background(), LaunchOptions{ProfileDir: profile})
	if err != nil {
		t.Fatalf("first NewBrowserContext: %v", err)
	}
	if _, cancel, _, err := NewBrowserContext(context.Background(), LaunchOptions{ProfileDir: profile}); err == nil {
		cancel()
		cancelFirst()
		t.Fatal("second NewBrowserContext succeeded while profile was locked")
	} else if !strings.Contains(err.Error(), "already in use") {
		cancelFirst()
		t.Fatalf("lock error = %q, want clear already-in-use error", err)
	}

	cancelFirst()
	cancelFirst() // cancel is intentionally idempotent.
	_, cancelThird, _, err := NewBrowserContext(context.Background(), LaunchOptions{ProfileDir: profile})
	if err != nil {
		t.Fatalf("reopen after cancel: %v", err)
	}
	cancelThird()
}

func TestPrivateContextDoesNotCreatePersistentProfile(t *testing.T) {
	root := t.TempDir()
	invalidConfigHome := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(invalidConfigHome, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", invalidConfigHome)

	ctx, cancel, info, err := NewBrowserContext(context.Background(), LaunchOptions{Private: true})
	if err != nil {
		t.Fatalf("private NewBrowserContext: %v", err)
	}
	if ctx == nil || cancel == nil {
		t.Fatal("private NewBrowserContext returned a nil context or cancel")
	}
	if !info.Private || info.Dir != "" || info.StatePath != "" || info.DownloadDir == "" {
		t.Fatalf("private ProfileInfo = %+v", info)
	}
	cancel()
	cancel()

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "not-a-directory" {
		t.Fatalf("private launch touched persistent config area: %v", entries)
	}
}

func TestPrivateProfileConflictDoesNotTouchPath(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "must-not-exist")
	_, _, _, err := NewBrowserContext(context.Background(), LaunchOptions{
		Private:    true,
		ProfileDir: profile,
	})
	if err == nil {
		t.Fatal("private launch with ProfileDir succeeded")
	}
	if _, statErr := os.Stat(profile); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting private launch touched profile: %v", statErr)
	}
}

func TestInvalidUserAgentDoesNotTouchProfile(t *testing.T) {
	tests := []string{
		"line one\r\nline two",
		"contains\x00control",
		strings.Repeat("a", maxUserAgentLen+1),
		string([]byte{0xff}),
	}
	for i, userAgent := range tests {
		profile := filepath.Join(t.TempDir(), "profile")
		_, _, _, err := NewBrowserContext(context.Background(), LaunchOptions{
			ProfileDir: profile,
			UserAgent:  userAgent,
		})
		if err == nil {
			t.Errorf("case %d: invalid UserAgent accepted", i)
		}
		if _, statErr := os.Stat(profile); !os.IsNotExist(statErr) {
			t.Errorf("case %d: invalid UserAgent touched profile: %v", i, statErr)
		}
	}
}

func TestProfilePathMustBeDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile-file")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := NewBrowserContext(context.Background(), LaunchOptions{ProfileDir: path})
	if err == nil {
		t.Fatal("NewBrowserContext accepted a regular file as ProfileDir")
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil || string(contents) != "do not replace" {
		t.Fatalf("profile file was changed: contents %q, error %v", contents, readErr)
	}
}

func requireUnixProfileLock(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("persistent profile locking is implemented with flock on darwin/linux")
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// A persistent profile keeps cookies and logins, not the last window's tabs:
// otherwise a killed browser comes back with its old tabs, one of them active,
// and the page this process opened sits in the background.
func TestDiscardRestoredSessionKeepsTheRestOfTheProfile(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Current Session", "Current Tabs", "Last Session", "Last Tabs"} {
		if err := os.WriteFile(filepath.Join(profile, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(profile, "Cookies")
	if err := os.WriteFile(keep, []byte("login"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := discardRestoredSession(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profile, "Sessions")); !os.IsNotExist(err) {
		t.Errorf("Sessions directory survived: %v", err)
	}
	for _, name := range []string{"Current Session", "Current Tabs", "Last Session", "Last Tabs"} {
		if _, err := os.Stat(filepath.Join(profile, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", name, err)
		}
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "login" {
		t.Errorf("Cookies changed: %q %v", data, err)
	}
}
