// Package httpapi exposes the link-shortener API and the small web UI.
package httpapi

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ObaidAbdullah16/shipit-platform/services/api/internal/store"
)

// Store is what the handlers need from the database.
type Store interface {
	Create(ctx context.Context, code, url string) (store.Link, error)
	URL(ctx context.Context, code string) (string, error)
	Recent(ctx context.Context, limit int) ([]store.Link, error)
	Daily(ctx context.Context, code string) ([]store.DayCount, error)
	Ping(ctx context.Context) error
}

// Cache is what the handlers need from Redis.
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	SetEX(ctx context.Context, key, value string, ttl time.Duration) error
	LPush(ctx context.Context, key, value string) (int64, error)
	Incr(ctx context.Context, key string) (int64, error)
	GetInt(ctx context.Context, key string) (int64, error)
	Ping(ctx context.Context) error
}

// Server wires handlers, dependencies and middleware.
type Server struct {
	Store           Store
	Cache           Cache
	Log             *slog.Logger
	Metrics         *Metrics
	Version         string
	PublicBaseURL   string
	ChaosFailHealth bool
	limiter         *limiter
	static          fs.FS
}

//go:embed web
var webFS embed.FS

// New creates a server. createPerMinute limits link creation per client IP.
func New(st Store, c Cache, log *slog.Logger, version, publicBase string, createPerMinute int, chaos bool) *Server {
	sub, _ := fs.Sub(webFS, "web")
	return &Server{Store: st, Cache: c, Log: log, Metrics: NewMetrics(version), Version: version,
		PublicBaseURL: strings.TrimRight(publicBase, "/"), ChaosFailHealth: chaos,
		limiter: newLimiter(createPerMinute, time.Minute), static: sub}
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("POST /api/links", s.createLink)
	mux.HandleFunc("GET /api/links", s.recent)
	mux.HandleFunc("GET /api/links/{code}/stats", s.stats)
	mux.HandleFunc("GET /r/{code}", s.redirect)
	mux.Handle("GET /", http.FileServerFS(s.static))
	return s.middleware(mux)
}

// ---------------------------------------------------------------- middleware

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func routeOf(r *http.Request) string {
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/r/"):
		return "/r/{code}"
	case strings.HasPrefix(p, "/api/links/") && strings.HasSuffix(p, "/stats"):
		return "/api/links/{code}/stats"
	case p == "/api/links", p == "/healthz", p == "/readyz", p == "/version", p == "/metrics":
		return p
	}
	return "/static"
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Amzn-Trace-Id")
		if reqID == "" {
			reqID = randomString(12)
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		sw.Header().Set("X-Request-Id", reqID)
		sw.Header().Set("X-Content-Type-Options", "nosniff")
		sw.Header().Set("Referrer-Policy", "no-referrer")
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic", "request_id", reqID, "panic", rec)
				http.Error(sw, "internal error", http.StatusInternalServerError)
			}
			route := routeOf(r)
			d := time.Since(start)
			s.Metrics.Inc("shipit_http_requests_total", "route", route, "method", r.Method, "code", http.StatusText(sw.status))
			s.Metrics.Observe(route, d)
			if route != "/healthz" && route != "/metrics" {
				s.Log.Info("request", "request_id", reqID, "method", r.Method, "path", r.URL.Path,
					"status", sw.status, "duration_ms", d.Milliseconds(), "ip", clientIP(r))
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// ------------------------------------------------------------------ handlers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	if s.ChaosFailHealth {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "failing on purpose (CHAOS_FAIL_HEALTH=1)"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"postgres": "ok", "redis": "ok"}
	status := http.StatusOK
	if err := s.Store.Ping(ctx); err != nil {
		checks["postgres"], status = err.Error(), http.StatusServiceUnavailable
	}
	if err := s.Cache.Ping(ctx); err != nil {
		checks["redis"], status = err.Error(), http.StatusServiceUnavailable
	}
	writeJSON(w, status, checks)
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.Metrics.Write(w)
}

const maxURL = 2048

// ValidateURL accepts only absolute http(s) URLs to public-looking hosts.
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURL {
		return "", errors.New("url must be 1-2048 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("url must start with http:// or https://")
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return "", errors.New("internal hosts are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return "", errors.New("private addresses are not allowed")
	}
	return u.String(), nil
}

func (s *Server) shortURL(r *http.Request, code string) string {
	base := s.PublicBaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/r/" + code
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many links created, try again in a minute"})
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON like {\"url\": \"https://...\"}"})
		return
	}
	target, err := ValidateURL(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	for attempt := 0; attempt < 5; attempt++ {
		code := randomString(7)
		link, err := s.Store.Create(r.Context(), code, target)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			s.Log.Error("create link", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the link"})
			return
		}
		_ = s.Cache.SetEX(r.Context(), "link:"+code, target, time.Hour)
		s.Metrics.Inc("shipit_links_created_total")
		writeJSON(w, http.StatusCreated, map[string]any{"code": link.Code, "url": link.URL, "short_url": s.shortURL(r, link.Code), "created_at": link.CreatedAt})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not allocate a code"})
}

func (s *Server) recent(w http.ResponseWriter, r *http.Request) {
	links, err := s.Store.Recent(r.Context(), 20)
	if err != nil {
		s.Log.Error("recent", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, links)
}

func validCode(code string) bool {
	if len(code) < 4 || len(code) > 16 {
		return false
	}
	for _, c := range code {
		if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	return true
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !validCode(code) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	target, err := s.Cache.Get(ctx, "link:"+code)
	if err == nil && target != "" {
		s.Metrics.Inc("shipit_cache_total", "result", "hit")
	} else {
		s.Metrics.Inc("shipit_cache_total", "result", "miss")
		target, err = s.Store.URL(ctx, code)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.Log.Error("lookup", "err", err)
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = s.Cache.SetEX(ctx, "link:"+code, target, time.Hour)
	}
	// Click events go to a Redis list; the Python worker aggregates them into Postgres.
	event, _ := json.Marshal(map[string]any{"code": code, "ts": time.Now().Unix()})
	if _, err := s.Cache.LPush(ctx, "clicks", string(event)); err == nil {
		_, _ = s.Cache.Incr(ctx, "pending:"+code)
	}
	s.Metrics.Inc("shipit_redirects_total")
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !validCode(code) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown code"})
		return
	}
	if _, err := s.Store.URL(r.Context(), code); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown code"})
		return
	}
	days, err := s.Store.Daily(r.Context(), code)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	pending, _ := s.Cache.GetInt(r.Context(), "pending:"+code)
	var total int64
	for _, d := range days {
		total += d.Clicks
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "processed": total, "pending": pending, "total": total + pending, "daily": days})
}

// --------------------------------------------------------------------- utils

const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/l/I

func randomString(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

func clientIP(r *http.Request) string {
	// The ALB appends the address it received the connection from to the END of
	// X-Forwarded-For. Anything before it was sent by the client and can be forged,
	// so the right-most entry is the only one we can trust (one proxy in front).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter is a fixed-window counter per key (enough for one task; use Redis for many).
type limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*bucket
}

type bucket struct {
	start time.Time
	n     int
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, seen: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.seen[key]
	if !ok || now.Sub(b.start) > l.window {
		if len(l.seen) > 10000 { // bound memory
			l.seen = map[string]*bucket{}
		}
		l.seen[key] = &bucket{start: now, n: 1}
		return true
	}
	if b.n >= l.limit {
		return false
	}
	b.n++
	return true
}
