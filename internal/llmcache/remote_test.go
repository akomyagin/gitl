package llmcache

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newKVServer starts an httptest.Server implementing the dumb KV protocol on
// a map keyed by URL path: GET returns the stored bytes or 404, PUT stores
// the body and returns 204.
func newKVServer(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	var store sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if v, ok := store.Load(r.URL.Path); ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(v.([]byte))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(r.Body); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			store.Store(r.URL.Path, buf.Bytes())
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &store
}

func newTestRemote(t *testing.T, baseURL, token string, ttl time.Duration) *remoteCache {
	t.Helper()
	c, err := newRemote(baseURL, token, ttl, 5*time.Second)
	if err != nil {
		t.Fatalf("newRemote: %v", err)
	}
	return c
}

func TestRemoteMissThenPutThenHit(t *testing.T) {
	srv, _ := newKVServer(t)
	// Trailing slash on the base URL must be tolerated.
	c := newTestRemote(t, srv.URL+"/", "", 24*time.Hour)
	key := testKey()

	if _, ok, err := c.Get(key); ok || err != nil {
		t.Fatalf("expected miss before Put, got ok=%v err=%v", ok, err)
	}

	want := sampleResponse()
	if err := c.Put(key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if got != want {
		t.Fatalf("mismatch: got %+v want %+v", got, want)
	}
}

func TestRemoteAuthHeader(t *testing.T) {
	const token = "test-bearer-token"
	var mu sync.Mutex
	seen := map[string][]string{} // method -> Authorization header values observed

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method] = append(seen[r.Method], r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	key := testKey()

	// With a token: both verbs must carry the bearer header.
	withAuth := newTestRemote(t, srv.URL, token, time.Hour)
	_, _, _ = withAuth.Get(key)
	_ = withAuth.Put(key, sampleResponse())

	// Without a token: no Authorization header on either verb.
	noAuth := newTestRemote(t, srv.URL, "", time.Hour)
	_, _, _ = noAuth.Get(key)
	_ = noAuth.Put(key, sampleResponse())

	mu.Lock()
	defer mu.Unlock()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		vals := seen[method]
		if len(vals) != 2 {
			t.Fatalf("%s: expected 2 requests, saw %d", method, len(vals))
		}
		if vals[0] != "Bearer "+token {
			t.Errorf("%s with token: Authorization = %q, want %q", method, vals[0], "Bearer "+token)
		}
		if vals[1] != "" {
			t.Errorf("%s without token: Authorization = %q, want empty", method, vals[1])
		}
	}
}

func TestRemote404IsMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	c := newTestRemote(t, srv.URL, "", time.Hour)
	resp, ok, err := c.Get(testKey())
	if ok || err != nil {
		t.Fatalf("404 must be (zero,false,nil), got ok=%v err=%v", ok, err)
	}
	if resp.Content != "" {
		t.Fatalf("404 must return the zero response, got %+v", resp)
	}
}

func TestRemoteServerError5xxDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := newTestRemote(t, srv.URL, "", time.Hour)
	if _, ok, err := c.Get(testKey()); ok || err != nil {
		t.Fatalf("5xx Get must degrade to (zero,false,nil), got ok=%v err=%v", ok, err)
	}
	if err := c.Put(testKey(), sampleResponse()); err != nil {
		t.Fatalf("5xx Put must return nil, got %v", err)
	}
}

func TestRemoteTimeoutDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // far beyond the 50ms client timeout
	}))
	t.Cleanup(srv.Close)

	c, err := newRemote(srv.URL, "", time.Hour, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("newRemote: %v", err)
	}
	if _, ok, gerr := c.Get(testKey()); ok || gerr != nil {
		t.Fatalf("timed-out Get must degrade to (zero,false,nil), got ok=%v err=%v", ok, gerr)
	}
	if perr := c.Put(testKey(), sampleResponse()); perr != nil {
		t.Fatalf("timed-out Put must return nil, got %v", perr)
	}
}

func TestRemoteExpiredEntryIsMiss(t *testing.T) {
	stale, err := json.Marshal(wireResponse{
		CachedAt: time.Now().UTC().Add(-48 * time.Hour),
		Content:  "old",
		Risk:     wireRisk{Level: "low"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(stale)
	}))
	t.Cleanup(srv.Close)

	// 24h TTL, entry is 48h old: the client-side TTL must turn a 200 into a miss.
	c := newTestRemote(t, srv.URL, "", 24*time.Hour)
	if _, ok, err := c.Get(testKey()); ok || err != nil {
		t.Fatalf("expired entry must be a miss, got ok=%v err=%v", ok, err)
	}
}

func TestRemoteTokenNeverLogged(t *testing.T) {
	const token = "super-secret-cache-token-do-not-log"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A server that always errors forces the warn paths on both verbs.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := newTestRemote(t, srv.URL, token, time.Hour)
	_, _, _ = c.Get(testKey())
	_ = c.Put(testKey(), sampleResponse())
	srv.Close() // now force a network-level error path too
	_, _, _ = c.Get(testKey())
	_ = c.Put(testKey(), sampleResponse())

	if out := buf.String(); strings.Contains(out, token) {
		t.Fatalf("bearer token leaked into logs:\n%s", out)
	}
}

// TestNewRemoteClampsZeroTimeout: defense in depth behind config validation —
// a non-positive timeout reaching newRemote directly must be clamped to the
// safe default, never left at 0 ("no timeout" in net/http, which could hang a
// review forever against an unresponsive server).
func TestNewRemoteClampsZeroTimeout(t *testing.T) {
	srv, _ := newKVServer(t)
	c, err := newRemote(srv.URL, "", time.Hour, 0)
	if err != nil {
		t.Fatalf("newRemote: %v", err)
	}
	if c.client.Timeout != 3*time.Second {
		t.Errorf("client.Timeout = %v, want the 3s clamp for a zero timeout", c.client.Timeout)
	}
}
