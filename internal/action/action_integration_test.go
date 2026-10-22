package action

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/getan/kitty-browse/internal/browser"
)

// TestNativeCDPEndToEnd is opt-in because it launches the locally installed
// Chromium. It never invokes an automation helper or requires network access.
func TestNativeCDPEndToEnd(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run with local Chromium")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "page.html")
	page := `<title>native action test</title><body><input id="field" value="old"><button id="button">Go</button></body>`
	if err := os.WriteFile(path, []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		path = "/" + filepath.ToSlash(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	allocator, closeAllocator, _, err := browser.NewBrowserContext(ctx, browser.LaunchOptions{ExecutablePath: chrome, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAllocator()
	sess, err := browser.Open(allocator, "file://"+filepath.ToSlash(path), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	fill, err := ParseCommand([]string{"fill", "#field", "native"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(ctx, fill, sess); err != nil {
		t.Fatal(err)
	}
	get, err := ParseCommand([]string{"get", "value", "#field"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(ctx, get, sess)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `"native"` {
		t.Fatalf("value = %s", result)
	}
	if _, err := Execute(ctx, Request{Action: "click", Params: []byte(`{"selector":"#field"}`)}, sess); err != nil {
		t.Fatal(err)
	}
	insert, err := ParseCommand([]string{"keyboard", "insert", " text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(ctx, insert, sess); err != nil {
		t.Fatal(err)
	}
	result, err = Execute(ctx, get, sess)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `"native text"` {
		t.Fatalf("inserted value = %s", result)
	}

	pdfResult, err := Execute(ctx, Request{Action: "pdf", Params: []byte(`{}`)}, sess)
	if err != nil {
		t.Fatal(err)
	}
	var pdf struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
		Bytes    int    `json:"bytes"`
	}
	if err := json.Unmarshal(pdfResult, &pdf); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(pdf.Data)
	if err != nil {
		t.Fatal(err)
	}
	if pdf.MimeType != "application/pdf" || pdf.Bytes != len(decoded) || !bytes.HasPrefix(decoded, []byte("%PDF-")) {
		t.Fatalf("invalid PDF result: mime=%q bytes=%d decoded=%d", pdf.MimeType, pdf.Bytes, len(decoded))
	}
	pdfPath := filepath.Join(dir, "page.pdf")
	if _, err := Execute(ctx, Request{Action: "pdf", Params: json.RawMessage(`{"path":` + strconv.Quote(pdfPath) + `}`)}, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(ctx, Request{Action: "pdf", Params: json.RawMessage(`{"path":` + strconv.Quote(pdfPath) + `}`)}, sess); err == nil {
		t.Fatal("PDF overwrite was accepted")
	}
	info, err := os.Stat(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("PDF mode = %o", info.Mode().Perm())
	}
}

func TestNativeDialogAndWaitEndToEnd(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("set KITTY_BROWSE_CHROME to run with local Chromium")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><title>dialog wait</title><body>ready</body>`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	allocator, closeAllocator, _, err := browser.NewBrowserContext(ctx, browser.LaunchOptions{ExecutablePath: chrome, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAllocator()
	sess, err := browser.Open(allocator, server.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	openDialog := func(script, wantType string, accept bool, prompt string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, runErr := Execute(ctx, Request{Action: "eval", Params: json.RawMessage(`{"script":` + strconv.Quote(script) + `}`)}, sess)
			done <- runErr
		}()
		deadline := time.Now().Add(3 * time.Second)
		for {
			result, statusErr := Execute(ctx, Request{Action: "dialog.status", Params: json.RawMessage(`{}`)}, sess)
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			var status struct {
				Open bool   `json:"open"`
				Type string `json:"type"`
			}
			if err := json.Unmarshal(result, &status); err != nil {
				t.Fatal(err)
			}
			if status.Open {
				if status.Type != wantType {
					t.Fatalf("dialog type = %q, want %q", status.Type, wantType)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("dialog did not open")
			}
			time.Sleep(10 * time.Millisecond)
		}
		action, params := "dialog.dismiss", json.RawMessage(`{}`)
		if accept {
			action = "dialog.accept"
			params = json.RawMessage(`{"promptText":` + strconv.Quote(prompt) + `}`)
		}
		if _, err := Execute(ctx, Request{Action: action, Params: params}, sess); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	openDialog(`window.promptResult = prompt("question", "default")`, "prompt", true, "answer")
	if _, err := Execute(ctx, Request{Action: "wait.function", Params: json.RawMessage(`{"script":"window.promptResult === 'answer'"}`)}, sess); err != nil {
		t.Fatal(err)
	}
	openDialog(`window.confirmResult = confirm("continue?")`, "confirm", false, "")
	if _, err := Execute(ctx, Request{Action: "wait.function", Params: json.RawMessage(`{"script":"window.confirmResult === false"}`)}, sess); err != nil {
		t.Fatal(err)
	}

	if _, err := Execute(ctx, Request{Action: "eval", Params: json.RawMessage(`{"script":"location.hash='ready'"}`)}, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(ctx, Request{Action: "wait.url", Params: json.RawMessage(`{"pattern":"#ready"}`)}, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(ctx, Request{Action: "wait.loadState", Params: json.RawMessage(`{"state":"domcontentloaded"}`)}, sess); err != nil {
		t.Fatal(err)
	}
}
