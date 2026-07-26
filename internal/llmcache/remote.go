package llmcache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/akomyagin/gitl/internal/llm"
)

// remoteCache is the opt-in shared L2 tier: a dumb HTTP KV store the USER
// hosts (gitl never ships or runs a service). Protocol:
//
//	GET {base_url}/{key}   → 200 + wireResponse JSON body = hit (client-side
//	                         TTL still applies), 404 = miss, anything else
//	                         (status, network error, timeout) = miss + warn.
//	PUT {base_url}/{key}   → body is wireResponse JSON, Content-Type
//	                         application/json; 200/201/204 = stored, anything
//	                         else = warn, treated as a failed store.
//
// key is the 64-char hex SHA-256 from Key(). When a bearer token is
// configured it is sent as "Authorization: Bearer <token>" on both verbs.
// Every failure degrades to a miss / no-op: this backend can never fail a
// review, and the token is never logged.
type remoteCache struct {
	base   string // validated, no trailing slash
	token  string // resolved from token_env; "" = no auth header
	ttl    time.Duration
	client *http.Client // short timeout
}

// maxRemoteBodyBytes caps how much of a GET response body is read — a
// defensive bound against a misbehaving server; real entries are far smaller.
const maxRemoteBodyBytes = 8 << 20 // 8 MiB

// newRemote validates baseURL and builds the remote tier. The Cache interface
// is deliberately context-free (this backend is optional and best-effort), so
// requests rely solely on client.Timeout for cancellation.
func newRemote(baseURL, token string, ttl, timeout time.Duration) (*remoteCache, error) {
	base := strings.TrimRight(baseURL, "/")
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse remote cache url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("remote cache url %q: must be an absolute http(s) URL", base)
	}
	// Defense-in-depth: a zero timeout means "no timeout" in net/http, which can
	// hang a review forever against an unresponsive server (requests use
	// context.Background()). config.validate() rejects timeout_ms<=0 when a URL is
	// set, but newRemote can be reached directly (tests, future callers), so clamp
	// a non-positive timeout to a safe default here too.
	const defaultRemoteTimeout = 3 * time.Second
	if timeout <= 0 {
		timeout = defaultRemoteTimeout
	}
	return &remoteCache{
		base:   base,
		token:  token,
		ttl:    ttl,
		client: &http.Client{Timeout: timeout},
	}, nil
}

// authorize adds the bearer token header when one is configured.
func (c *remoteCache) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// Get fetches the entry for key. Every failure mode — network error, timeout,
// unexpected status, malformed body — is a warn-and-miss, never a non-nil
// error: the caller must fall through to the LLM, not fail the review.
func (c *remoteCache) Get(key string) (llm.Response, bool, error) {
	resp, _, ok := c.getWithCachedAt(key)
	return resp, ok, nil
}

// getWithCachedAt is Get plus the entry's stored CachedAt — used only by
// tiered.Get to backfill L1 with the original timestamp. Same warn-and-miss
// degradation as Get.
func (c *remoteCache) getWithCachedAt(key string) (llm.Response, time.Time, bool) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.base+"/"+key, nil)
	if err != nil {
		slog.Warn("remote cache get failed", "err", err)
		return llm.Response{}, time.Time{}, false
	}
	c.authorize(req)

	res, err := c.client.Do(req)
	if err != nil {
		slog.Warn("remote cache get failed", "err", err)
		return llm.Response{}, time.Time{}, false
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
		// fall through to the body
	case http.StatusNotFound:
		slog.Debug("remote cache miss", "key", key[:min(len(key), 12)])
		return llm.Response{}, time.Time{}, false
	default:
		slog.Warn("remote cache get failed", "status", res.StatusCode)
		return llm.Response{}, time.Time{}, false
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxRemoteBodyBytes))
	if err != nil {
		slog.Warn("remote cache get failed", "err", err)
		return llm.Response{}, time.Time{}, false
	}
	resp, cachedAt, ok, err := decodeResponseAt(body, c.ttl)
	if err != nil {
		slog.Warn("remote cache get failed", "err", err)
		return llm.Response{}, time.Time{}, false
	}
	return resp, cachedAt, ok // ok == false means the entry exceeded the client-side TTL
}

// Put stores resp under key. A failed store is warn-only and returns nil —
// the response was already produced; losing a cache write must never fail
// the review.
func (c *remoteCache) Put(key string, resp llm.Response) error {
	data, err := encodeResponse(resp)
	if err != nil {
		slog.Warn("remote cache put failed", "err", err)
		return nil
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, c.base+"/"+key, bytes.NewReader(data))
	if err != nil {
		slog.Warn("remote cache put failed", "err", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	res, err := c.client.Do(req)
	if err != nil {
		slog.Warn("remote cache put failed", "err", err)
		return nil
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxRemoteBodyBytes))

	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	default:
		slog.Warn("remote cache put failed", "status", res.StatusCode)
		return nil
	}
}
