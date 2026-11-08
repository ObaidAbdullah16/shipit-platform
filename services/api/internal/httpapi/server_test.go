package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ObaidAbdullah16/shipit-platform/services/api/internal/store"
)

type fakeStore struct {
	mu      sync.Mutex
	links   map[string]string
	daily   map[string][]store.DayCount
	failing bool
	collide int // number of Create calls that return ErrConflict first
}

func newFakeStore() *fakeStore {
	return &fakeStore{links: map[string]string{}, daily: map[string][]store.DayCount{}}
}

func (f *fakeStore) Create(_ context.Context, code, url string) (store.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return store.Link{}, errors.New("db down")
	}
	if f.collide > 0 {
		f.collide--
		return store.Link{}, store.ErrConflict
	}
	f.links[code] = url
	return store.Link{Code: code, URL: url, CreatedAt: time.Now()}, nil
}

func (f *fakeStore) URL(_ context.Context, code string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return "", errors.New("db down")
	}
	u, ok := f.links[code]
	if !ok {
		return "", store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) Recent(context.Context, int) ([]store.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Link{}
	for c, u := range f.links {
		out = append(out, store.Link{Code: c, URL: u})
	}
	return out, nil
}

func (f *fakeStore) Daily(_ context.Context, code string) ([]store.DayCount, error) {
	return f.daily[code], nil
}

func (f *fakeStore) Ping(context.Context) error {
	if f.failing {
		return errors.New("db down")
	}
	return nil
}

type fakeCache struct {
	mu    sync.Mutex
	kv    map[string]string
	lists map[string][]string
	down  bool
}

func newFakeCache() *fakeCache {
	return &fakeCache{kv: map[string]string{}, lists: map[string][]string{}}
}

var errDown = errors.New("redis down")

func (c *fakeCache) Get(_ context.Context, k string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return "", errDown
	}
	v, ok := c.kv[k]
	if !ok {
		return "", errors.New("nil")
	}
	return v, nil
}

func (c *fakeCache) SetEX(_ context.Context, k, v string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return errDown
	}
	c.kv[k] = v
	return nil
}

func (c *fakeCache) LPush(_ context.Context, k, v string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return 0, errDown
	}
	c.lists[k] = append([]string{v}, c.lists[k]...)
	return int64(len(c.lists[k])), nil
}

func (c *fakeCache) Incr(_ context.Context, k string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return 0, errDown
	}
	var n int64
	if v, ok := c.kv[k]; ok {
		json.Unmarshal([]byte(v), &n) //nolint:errcheck
	}
	n++
	b, _ := json.Marshal(n)
	c.kv[k] = string(b)
	return n, nil
}

func (c *fakeCache) GetInt(_ context.Context, k string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	if v, ok := c.kv[k]; ok {
		json.Unmarshal([]byte(v), &n) //nolint:errcheck
	}
	return n, nil
}

func (c *fakeCache) Ping(context.Context) error {
	if c.down {
		return errDown
	}
	return nil
}

func newTestServer(chaos bool) (*Server, *fakeStore, *fakeCache) {
	return newLimitedServer(chaos, 100)
}

func newLimitedServer(chaos bool, perMinute int) (*Server, *fakeStore, *fakeCache) {
	st, c := newFakeStore(), newFakeCache()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, c, log, "test-sha", "", perMinute, chaos), st, c
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndChaos(t *testing.T) {
	s, _, _ := newTestServer(false)
	if rec := do(t, s.Handler(), "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	s2, _, _ := newTestServer(true)
	if rec := do(t, s2.Handler(), "GET", "/healthz", ""); rec.Code != 500 {
		t.Fatalf("chaos healthz = %d, want 500", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	s, st, c := newTestServer(false)
	h := s.Handler()
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("readyz = %d", rec.Code)
	}
	c.down = true
	rec := do(t, h, "GET", "/readyz", "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "redis down") {
		t.Fatalf("readyz with redis down = %d %s", rec.Code, rec.Body)
	}
	c.down, st.failing = false, true
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != 503 {
		t.Fatalf("readyz with db down = %d", rec.Code)
	}
}

func TestCreateRedirectStats(t *testing.T) {
	s, st, c := newTestServer(false)
	h := s.Handler()
	rec := do(t, h, "POST", "/api/links", `{"url":"https://example.com/docs?a=1"}`)
	if rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	code := created["code"].(string)
	if !validCode(code) || !strings.HasSuffix(created["short_url"].(string), "/r/"+code) {
		t.Fatalf("bad create response: %v", created)
	}

	// Cache hit path.
	rec = do(t, h, "GET", "/r/"+code, "")
	if rec.Code != 302 || rec.Header().Get("Location") != "https://example.com/docs?a=1" {
		t.Fatalf("redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Cache miss path falls back to the database.
	delete(c.kv, "link:"+code)
	if rec = do(t, h, "GET", "/r/"+code, ""); rec.Code != 302 {
		t.Fatalf("redirect after cache miss = %d", rec.Code)
	}
	if len(c.lists["clicks"]) != 2 {
		t.Fatalf("click events = %d, want 2", len(c.lists["clicks"]))
	}

	st.daily[code] = []store.DayCount{{Day: "2026-10-01", Clicks: 5}}
	rec = do(t, h, "GET", "/api/links/"+code+"/stats", "")
	var stats map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &stats)
	if rec.Code != 200 || stats["processed"].(float64) != 5 || stats["pending"].(float64) != 2 || stats["total"].(float64) != 7 {
		t.Fatalf("stats = %d %v", rec.Code, stats)
	}

	if rec = do(t, h, "GET", "/r/zzzzzzz", ""); rec.Code != 404 {
		t.Fatalf("unknown code = %d", rec.Code)
	}
	if rec = do(t, h, "GET", "/r/bad!code", ""); rec.Code != 404 {
		t.Fatalf("invalid code = %d", rec.Code)
	}
	if rec = do(t, h, "GET", "/api/links/zzzzzzz/stats", ""); rec.Code != 404 {
		t.Fatalf("stats unknown = %d", rec.Code)
	}
	if rec = do(t, h, "GET", "/api/links", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), code) {
		t.Fatalf("recent = %d %s", rec.Code, rec.Body)
	}
}

func TestRedirectSurvivesRedisOutage(t *testing.T) {
	s, st, c := newTestServer(false)
	st.links["abcd234"] = "https://example.org/"
	c.down = true
	rec := do(t, s.Handler(), "GET", "/r/abcd234", "")
	if rec.Code != 302 {
		t.Fatalf("redirect with redis down = %d (should degrade, not fail)", rec.Code)
	}
}

func TestCreateValidationAndErrors(t *testing.T) {
	s, st, _ := newTestServer(false)
	h := s.Handler()
	for _, body := range []string{`not json`, `{"url":"ftp://x.org"}`, `{"url":"http://127.0.0.1/admin"}`,
		`{"url":"http://169.254.169.254/latest/meta-data"}`, `{"url":"http://localhost:8080"}`, `{"url":""}`} {
		if rec := do(t, h, "POST", "/api/links", body); rec.Code != 400 {
			t.Fatalf("%s -> %d, want 400", body, rec.Code)
		}
	}
	st.collide = 2 // two collisions, then success
	if rec := do(t, h, "POST", "/api/links", `{"url":"https://example.com"}`); rec.Code != 201 {
		t.Fatalf("create after collisions = %d", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	s, _, _ := newLimitedServer(false, 3)
	h := s.Handler()
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, do(t, h, "POST", "/api/links", `{"url":"https://example.com"}`).Code)
	}
	if codes[2] != 201 || codes[3] != 429 {
		t.Fatalf("rate limit codes = %v", codes)
	}
}

func TestDatabaseErrorIs500(t *testing.T) {
	s, st, _ := newTestServer(false)
	st.failing = true
	if rec := do(t, s.Handler(), "POST", "/api/links", `{"url":"https://example.com"}`); rec.Code != 500 {
		t.Fatalf("create with db down = %d", rec.Code)
	}
}

func TestMetricsAndStatic(t *testing.T) {
	s, _, _ := newTestServer(false)
	h := s.Handler()
	do(t, h, "GET", "/healthz", "")
	rec := do(t, h, "GET", "/metrics", "")
	body := rec.Body.String()
	for _, want := range []string{`shipit_build_info{version="test-sha"} 1`, `shipit_http_requests_total{route="/healthz",method="GET",code="OK"} 1`,
		`shipit_http_request_duration_seconds_bucket{route="/healthz",le="+Inf"} 1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	rec = do(t, h, "GET", "/", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ShipIt Links") {
		t.Fatalf("index = %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("security headers missing")
	}
	if rec = do(t, h, "GET", "/version", ""); !strings.Contains(rec.Body.String(), "test-sha") {
		t.Fatalf("version = %s", rec.Body)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q", got)
	}
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := clientIP(r); got != "198.51.100.9" {
		t.Fatalf("clientIP with XFF = %q", got)
	}
	// A client that sends its own X-Forwarded-For can't choose its rate-limit key:
	// the ALB appends the real address last.
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.9")
	if got := clientIP(r); got != "198.51.100.9" {
		t.Fatalf("spoofed XFF: clientIP = %q, want the ALB-appended address", got)
	}
}
