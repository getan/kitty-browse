package browserhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const SharedProtocol = "kitty-browse-views-v1"

// SharedOptions is supplied only by the trusted embedder, never a page or model.
// StateDir holds an owner-only credential outside the agent's file boundary;
// RuntimeDir holds short Unix sockets. One server owns a dedicated profile.
type SharedOptions struct {
	Browser              Options
	StateDir, RuntimeDir string
	IdleTimeout          time.Duration
}

// ViewID binds a conversation and an optional independently opened instance.
func ViewID(conversation, instance string) string {
	sum := sha256.Sum256([]byte(conversation + "\x00" + instance))
	return hex.EncodeToString(sum[:12])
}

func SharedSocket(dir string) string   { return filepath.Join(dir, "hub.sock") }
func ViewSocket(dir, id string) string { return filepath.Join(dir, id+".sock") }

type sharedView struct {
	ID          string `json:"id"`
	Socket      string `json:"socket"`
	BrowserID   string `json:"browserId"`
	OperatorKey string `json:"operatorKey"`
}

// RemoteView is a trusted operator connection. Do not expose its credential to
// tools, logs or transcripts. The ordinary Socket serves automation only.
type RemoteView struct {
	Socket, BrowserID string
	id, key, hub      string
}

type sharedRequest struct {
	ID           string           `json:"id"`
	URL          string           `json:"url,omitempty"`
	ProxyServer  string           `json:"proxyServer,omitempty"`
	Input        *OperatorInput   `json:"input,omitempty"`
	Action       json.RawMessage  `json:"action,omitempty"`
	Continuation *ContinueRequest `json:"continuation,omitempty"`
	Challenge    string           `json:"challenge,omitempty"`
}

type sharedRecord struct {
	view *Host
	key  string
	seen time.Time
}

type sharedServer struct {
	mu       sync.Mutex
	launchMu sync.Mutex
	opts     SharedOptions
	token    string
	hub      *Hub
	records  map[string]sharedRecord
	pending  int
	closing  bool
	last     time.Time
	start    func(context.Context, Options) (*Hub, error)
}

// ServeShared runs a single profile owner. Its advisory runtime lock prevents
// concurrent starts; normal shutdown flushes the profile after closing views.
func ServeShared(ctx context.Context, opts SharedOptions) error {
	if !filepath.IsAbs(opts.StateDir) || !filepath.IsAbs(opts.RuntimeDir) || opts.Browser.Private || !filepath.IsAbs(opts.Browser.ProfileDir) {
		return errors.New("shared browser requires dedicated absolute state/runtime paths and a persistent profile")
	}
	for _, dir := range []string{opts.StateDir, opts.RuntimeDir} {
		if err := secureSharedDir(dir); err != nil {
			return err
		}
	}
	lock, err := sharedLock(filepath.Join(opts.RuntimeDir, "hub.lock"))
	if err != nil {
		return err
	}
	defer lock()
	socket := SharedSocket(opts.RuntimeDir)
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("shared endpoint is not a socket")
		}
		conn, dialErr := net.DialTimeout("unix", socket, time.Second)
		if dialErr == nil {
			conn.Close()
			return errors.New("shared browser already running")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
			return dialErr
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	token := rand.Text() + rand.Text()
	if err := writeSharedToken(filepath.Join(opts.StateDir, "hub.auth"), token); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if u, ok := listener.(*net.UnixListener); ok {
		u.SetUnlinkOnClose(false)
	}
	info, err := os.Lstat(socket)
	if err != nil {
		listener.Close()
		return err
	}
	defer func() {
		listener.Close()
		if current, err := os.Lstat(socket); err == nil && os.SameFile(info, current) {
			_ = os.Remove(socket)
		}
	}()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	lifetime, stopLifetime := context.WithCancel(ctx)
	defer stopLifetime()
	s := &sharedServer{opts: opts, token: token, records: make(map[string]sharedRecord), last: time.Now()}
	s.start = func(start context.Context, options Options) (*Hub, error) { return startHub(start, lifetime, options) }
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		stopLifetime()
		_ = server.Close()
		s.launchMu.Lock()
		defer s.launchMu.Unlock()
		if s.hub != nil {
			_ = s.hub.Close()
		}
	}()
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case <-tick.C:
			s.expireViews(time.Now())
			s.mu.Lock()
			idle := s.pending == 0 && len(s.records) == 0 && time.Since(s.last) >= opts.IdleTimeout
			if idle {
				s.closing = true
			}
			s.mu.Unlock()
			if idle {
				return nil
			}
		}
	}
}

func secureSharedDir(dir string) error {
	for p := filepath.Clean(dir); p != filepath.Dir(p); p = filepath.Dir(p) {
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("shared state traverses symlink: %s", p)
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || !sharedOwned(st) {
		return errors.New("shared directory is not owned by this user")
	}
	return os.Chmod(dir, 0700)
}

func writeSharedToken(path, token string) error {
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || !sharedOwned(st)) {
		return errors.New("invalid shared credential file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(token); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func validViewID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func (s *sharedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		_ = json.NewEncoder(w).Encode(map[string]string{"protocol": SharedProtocol})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "unsupported method", 405)
		return
	}
	var req sharedRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF || !validViewID(req.ID) {
		http.Error(w, "invalid request", 400)
		return
	}
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	record, found := s.records[req.ID]
	master := s.token != "" && subtle.ConstantTimeCompare([]byte(key), []byte(s.token)) == 1
	operator := found && record.key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(record.key)) == 1
	if !master && !operator {
		s.mu.Unlock()
		http.Error(w, "unauthorized", 403)
		return
	}
	if s.closing {
		s.mu.Unlock()
		http.Error(w, "shared browser is shutting down; inspect before reopening", 409)
		return
	}
	s.pending++
	s.last = time.Now()
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.pending--; s.last = time.Now(); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	switch r.URL.Path {
	case "/open", "/reconnect":
		if !master {
			http.Error(w, "unauthorized", 403)
			return
		}
		if r.URL.Path == "/reconnect" {
			if !found {
				http.Error(w, "view unavailable", 404)
				return
			}
			_ = json.NewEncoder(w).Encode(sharedView{ID: req.ID, Socket: record.view.Socket(), BrowserID: record.view.bridge.ID, OperatorKey: record.key})
			return
		}
		view, err := s.open(ctx, req)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		_ = json.NewEncoder(w).Encode(view)
	case "/input":
		if !operator || req.Input == nil {
			http.Error(w, "operator connection required", 403)
			return
		}
		if err := record.view.SendOperatorInput(ctx, *req.Input); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	case "/operator-action":
		if !operator {
			http.Error(w, "operator connection required", 403)
			return
		}
		raw, err := record.view.OperatorAction(ctx, req.Action)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		_, _ = w.Write(raw)
	case "/continue", "/continuation-outcome", "/attest":
		if !operator {
			http.Error(w, "operator connection required", 403)
			return
		}
		if r.URL.Path == "/attest" {
			if len(req.Challenge) < 16 || len(req.Challenge) > 128 {
				http.Error(w, "attestation challenge required", 400)
				return
			}
			mode := "direct"
			if s.opts.Browser.ProxyServer != "" {
				mode = "proxy"
			}
			_ = json.NewEncoder(w).Encode(ViewAttestation{ViewID: req.ID, BrowserID: record.view.bridge.ID, InstanceEpoch: record.view.bridge.ID, Socket: record.view.Socket(), LoginProfile: s.opts.Browser.LoginProfile, ProxyMode: mode, ChromiumSandbox: s.opts.Browser.RequireSandbox, Challenge: req.Challenge, RuntimeEnvelope: "browser_runtime", RuntimePolicyDigest: RuntimePolicyDigest(s.opts.Browser)})
			return
		}
		if req.Continuation == nil {
			http.Error(w, "continuation required", 400)
			return
		}
		if r.URL.Path == "/continuation-outcome" {
			_ = json.NewEncoder(w).Encode(map[string]string{"outcome": record.view.bridge.ContinuationOutcome(*req.Continuation)})
			return
		}
		if err := record.view.ContinueChecked(ctx, *req.Continuation); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		_ = json.NewEncoder(w).Encode(record.view.bridge.Interaction())
	case "/heartbeat":
		if !operator {
			http.Error(w, "operator connection required", 403)
			return
		}
		s.mu.Lock()
		if current, ok := s.records[req.ID]; ok && current.view == record.view {
			current.seen = time.Now()
			s.records[req.ID] = current
		}
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func (s *sharedServer) open(ctx context.Context, req sharedRequest) (sharedView, error) {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	s.mu.Lock()
	existing, exists := s.records[req.ID]
	count := len(s.records)
	s.mu.Unlock()
	if req.ProxyServer != s.opts.Browser.ProxyServer {
		return sharedView{}, errors.New("shared profile is running with another proxy; close its views before changing the route")
	}
	if exists {
		select {
		case <-existing.view.done:
			return sharedView{}, errors.New("view is closing; inspect before reopening")
		default:
		}
		return sharedView{ID: req.ID, Socket: existing.view.Socket(), BrowserID: existing.view.bridge.ID, OperatorKey: existing.key}, nil
	}
	if count >= 64 {
		return sharedView{}, errors.New("browser view limit reached")
	}
	if s.hub == nil {
		hub, err := s.start(ctx, s.opts.Browser)
		if err != nil {
			return sharedView{}, err
		}
		s.hub = hub
	}
	socket := ViewSocket(s.opts.RuntimeDir, req.ID)
	if st, err := os.Lstat(socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return sharedView{}, errors.New("view endpoint is not a socket")
		}
		conn, e := net.DialTimeout("unix", socket, time.Second)
		if e == nil {
			conn.Close()
			return sharedView{}, errors.New("view endpoint is already live")
		}
		if !errors.Is(e, syscall.ECONNREFUSED) && !errors.Is(e, os.ErrNotExist) {
			return sharedView{}, e
		}
		if err := os.Remove(socket); err != nil {
			return sharedView{}, err
		}
	} else if !os.IsNotExist(err) {
		return sharedView{}, err
	}
	view, err := s.hub.OpenView(ctx, ViewOptions{Socket: socket, URL: req.URL})
	if err != nil {
		return sharedView{}, err
	}
	key := rand.Text() + rand.Text()
	s.mu.Lock()
	s.records[req.ID] = sharedRecord{view: view, key: key, seen: time.Now()}
	s.mu.Unlock()
	go func() {
		_ = view.Wait()
		s.mu.Lock()
		if record := s.records[req.ID]; record.view == view {
			delete(s.records, req.ID)
		}
		s.last = time.Now()
		s.mu.Unlock()
	}()
	return sharedView{ID: req.ID, Socket: socket, BrowserID: view.bridge.ID, OperatorKey: key}, nil
}

func (s *sharedServer) expireViews(now time.Time) {
	s.mu.Lock()
	var expired []*Host
	for id, record := range s.records {
		if !record.seen.IsZero() && now.Sub(record.seen) > 2*time.Minute {
			expired = append(expired, record.view)
			delete(s.records, id)
		}
	}
	s.mu.Unlock()
	for _, view := range expired {
		_ = view.Close()
	}
}

// KeepAlive maintains this view while a frontend owns it. A crashed frontend
// cannot leak an immortal Chromium view: the owner expires its lease after two
// minutes. It never retries a failed input or opens a replacement view.
func (v *RemoteView) KeepAlive(ctx context.Context) func() {
	life, cancel := context.WithCancel(ctx)
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			op, stop := context.WithTimeout(life, 3*time.Second)
			_, err := sharedCall(op, v.hub, v.key, "/heartbeat", sharedRequest{ID: v.id})
			stop()
			if err != nil {
				return
			}
			select {
			case <-life.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return cancel
}

// OpenShared authenticates a trusted frontend and opens/reconnects only its
// conversation's view. The returned operator capability must remain private.
func OpenShared(ctx context.Context, opts SharedOptions, id, url string) (*RemoteView, error) {
	return connectShared(ctx, opts, id, url, false)
}

// ReconnectShared never launches, navigates, changes proxy or creates a view.
func ReconnectShared(ctx context.Context, opts SharedOptions, id string) (*RemoteView, error) {
	return connectShared(ctx, opts, id, "", true)
}

func connectShared(ctx context.Context, opts SharedOptions, id, url string, reconnect bool) (*RemoteView, error) {
	data, err := readSharedToken(filepath.Join(opts.StateDir, "hub.auth"))
	if err != nil {
		return nil, err
	}
	path := "/open"
	if reconnect {
		path = "/reconnect"
	}
	raw, err := sharedCall(ctx, SharedSocket(opts.RuntimeDir), string(data), path, sharedRequest{ID: id, URL: url, ProxyServer: opts.Browser.ProxyServer})
	if err != nil {
		return nil, err
	}
	var out sharedView
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.ID != id || out.Socket != ViewSocket(opts.RuntimeDir, id) || out.BrowserID == "" || out.OperatorKey == "" {
		return nil, errors.New("shared browser returned an invalid view identity")
	}
	return &RemoteView{Socket: out.Socket, BrowserID: out.BrowserID, id: id, key: out.OperatorKey, hub: SharedSocket(opts.RuntimeDir)}, nil
}

func readSharedToken(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || !sharedOwned(st) || st.Size() > 256 {
		return nil, errors.New("invalid shared credential permissions")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, errors.New("shared credential changed")
	}
	return io.ReadAll(io.LimitReader(f, 257))
}

func SharedReady(ctx context.Context, runtimeDir string) bool {
	raw, err := sharedCall(ctx, SharedSocket(runtimeDir), "", "/health", nil)
	if err != nil {
		return false
	}
	var out struct {
		Protocol string `json:"protocol"`
	}
	return json.Unmarshal(raw, &out) == nil && out.Protocol == SharedProtocol
}

func (v *RemoteView) SendOperatorInput(ctx context.Context, in OperatorInput) error {
	if in.BrowserID != v.BrowserID {
		return errors.New("operator input belongs to another view")
	}
	_, err := sharedCall(ctx, v.hub, v.key, "/input", sharedRequest{ID: v.id, Input: &in})
	return err
}

func (v *RemoteView) OperatorAction(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return sharedCall(ctx, v.hub, v.key, "/operator-action", sharedRequest{ID: v.id, Action: raw})
}

// Close releases only this conversation's view, never the profile owner or
// another view. The broker exits after the last view and an idle grace period.
func (v *RemoteView) Close() error {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	raw, _ := json.Marshal(map[string]any{"id": "close-view", "action": "quit", "params": map[string]any{}, "target": map[string]string{"browserId": v.BrowserID}})
	_, err := v.OperatorAction(ctx, raw)
	return err
}

func sharedCall(ctx context.Context, socket, key, path string, body any) ([]byte, error) {
	st, err := os.Lstat(socket)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0077 != 0 || !sharedOwned(st) {
		return nil, errors.New("untrusted shared browser socket")
	}
	var encoded []byte
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	transport := &http.Transport{Proxy: nil, DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(c, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, errors.New("shared browser reply exceeds limit")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("shared browser: %s", strings.TrimSpace(string(data)))
	}
	return data, nil
}
