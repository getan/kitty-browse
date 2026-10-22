package screenshot

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return data.Bytes()
}

func TestSaveValidPNGAndNestedDirectory(t *testing.T) {
	data := testPNG(t, 7, 5)
	path := filepath.Join(t.TempDir(), "private", "nested", "shot.png")
	info, err := Save(path, data)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	wantPath, _ := filepath.Abs(path)
	if info.Path != wantPath || info.Width != 7 || info.Height != 5 || info.Bytes != len(data) {
		t.Fatalf("Info = %+v, want path=%q width=7 height=5 bytes=%d", info, wantPath, len(data))
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(written, data) {
		t.Fatal("saved bytes differ from input")
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %04o, want 0600", got)
	}
	for _, dir := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat directory: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory permissions = %04o, want 0700", got)
		}
	}
}

func TestSaveRejectsInvalidPNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unused", "shot.png")
	for name, data := range map[string][]byte{
		"plain bytes": []byte("not a png"),
		"header only": append([]byte(nil), pngSignature...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Save(path, data); err == nil {
				t.Fatal("Save succeeded, want error")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("destination exists after failed Save: %v", err)
			}
		})
	}
}

func TestSaveRefusesExistingFileAndSymlink(t *testing.T) {
	data := testPNG(t, 1, 1)
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.png")
	if err := os.WriteFile(existing, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(existing, data); err == nil {
		t.Fatal("Save over existing file succeeded")
	}
	got, _ := os.ReadFile(existing)
	if string(got) != "keep me" {
		t.Fatalf("existing contents changed to %q", got)
	}

	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(link, data); err == nil {
		t.Fatal("Save over symlink succeeded")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target unexpectedly created: %v", err)
	}
}

func TestSaveBadParentPath(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(filepath.Join(parent, "shot.png"), testPNG(t, 1, 1)); err == nil {
		t.Fatal("Save with non-directory parent succeeded")
	}
}

func TestAutoPath(t *testing.T) {
	dir := filepath.Join("some", "directory")
	first := AutoPath(dir, false)
	second := AutoPath(dir, true)
	if first == second {
		t.Fatalf("AutoPath returned duplicate path %q", first)
	}
	if filepath.Dir(first) != dir || !strings.HasPrefix(filepath.Base(first), "kitty-browse-") || !strings.HasSuffix(first, ".png") {
		t.Fatalf("unexpected viewport path %q", first)
	}
	if !strings.HasSuffix(second, "-full.png") {
		t.Fatalf("unexpected full-page path %q", second)
	}
}
