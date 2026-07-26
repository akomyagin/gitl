// Package llmcache is a content-addressed cache for LLM review responses.
// Keys hash every request parameter that can change the response (provider,
// model, endpoint, Azure coordinates, sampling parameters, prompts — see
// KeyParams) so no parameter change ever produces a false hit; entries expire
// by TTL. Two backends implement the Cache contract: the on-disk store
// (disk.go, always the L1 tier) and an opt-in remote HTTP KV store (remote.go,
// the L2 tier for shared/CI use); tiered composes them. The package depends
// only on the standard library.
package llmcache

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/akomyagin/gitl/internal/llm"
)

// Cache is the read/store contract for LLM review responses. The on-disk store
// (disk.go) and the remote HTTP KV store (remote.go) both implement it; tiered
// composes them. Get's bool reports a hit; a non-nil error is a real backend
// failure (callers degrade to "no cache", never fail the review over it).
type Cache interface {
	Get(key string) (llm.Response, bool, error)
	Put(key string, resp llm.Response) error
}

// nopCache is the zero-value cache: always a miss, Put is a no-op. Returned by
// the constructor when no backend is configured, so call sites never nil-check.
type nopCache struct{}

func (nopCache) Get(string) (llm.Response, bool, error) { return llm.Response{}, false, nil }
func (nopCache) Put(string, llm.Response) error         { return nil }

// wireResponse is the stable representation shared by every backend: the
// on-disk file format AND the remote HTTP body format are this exact JSON.
// llm.Response has no JSON tags, so it is never marshaled directly — this type
// pins the format.
type wireResponse struct {
	CachedAt time.Time `json:"cached_at"`
	Content  string    `json:"content"`
	Risk     wireRisk  `json:"risk"`
}

type wireRisk struct {
	Level     string `json:"level"`
	Summary   string `json:"summary"`
	Heuristic bool   `json:"heuristic"`
}

// encodeResponse marshals resp into the wireResponse JSON with CachedAt set to
// now (UTC). Both the disk and remote backends store exactly these bytes.
func encodeResponse(resp llm.Response) ([]byte, error) {
	return encodeResponseAt(resp, time.Now())
}

// encodeResponseAt marshals resp with an explicit CachedAt (UTC). encodeResponse
// is the now-wrapper that stamps the current time — used by every normal Put;
// encodeResponseAt exists so tiered.Get can backfill L1 while PRESERVING the
// remote entry's original CachedAt (a fresh Put would reset the TTL clock and
// let a near-expired remote entry live ~2×TTL locally).
func encodeResponseAt(resp llm.Response, cachedAt time.Time) ([]byte, error) {
	w := wireResponse{
		CachedAt: cachedAt.UTC(),
		Content:  resp.Content,
		Risk: wireRisk{
			Level:     resp.Risk.Level,
			Summary:   resp.Risk.Summary,
			Heuristic: resp.Risk.Heuristic,
		},
	}
	data, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("marshal cache entry: %w", err)
	}
	return data, nil
}

// decodeResponse parses wireResponse JSON back into an llm.Response. It
// reports (zero, false, nil) when the entry is older than ttl — both backends
// enforce the same client-side TTL — and a non-nil error only on malformed
// JSON.
func decodeResponse(data []byte, ttl time.Duration) (llm.Response, bool, error) {
	resp, _, ok, err := decodeResponseAt(data, ttl)
	return resp, ok, err
}

// decodeResponseAt is decodeResponse plus the entry's stored CachedAt, needed
// by tiered.Get to backfill L1 with the ORIGINAL timestamp. On a miss (expired)
// or parse error the returned time is the zero Time and callers ignore it.
func decodeResponseAt(data []byte, ttl time.Duration) (llm.Response, time.Time, bool, error) {
	var w wireResponse
	if err := json.Unmarshal(data, &w); err != nil {
		return llm.Response{}, time.Time{}, false, fmt.Errorf("parse cache entry: %w", err)
	}
	if time.Since(w.CachedAt) > ttl {
		return llm.Response{}, w.CachedAt, false, nil
	}
	return llm.Response{
		Content: w.Content,
		Risk: llm.Risk{
			Level:     w.Risk.Level,
			Summary:   w.Risk.Summary,
			Heuristic: w.Risk.Heuristic,
		},
	}, w.CachedAt, true, nil
}

// tiered composes the local disk cache (L1) with the remote HTTP KV cache
// (L2): reads check local first and backfill it on a remote hit; writes go to
// both. Remote interactions are best-effort — a remote failure never surfaces
// to the caller — while a local (L1) Put error IS returned, so disk-only and
// tiered modes report disk write failures identically.
type tiered struct {
	local  Cache // diskCache (may be nop when the disk is unavailable)
	remote Cache // remoteCache, or nil when not configured
}

func (t tiered) Get(key string) (llm.Response, bool, error) {
	if r, ok, _ := t.local.Get(key); ok {
		return r, true, nil
	}
	if t.remote != nil {
		// Backfill L1 with the remote entry's ORIGINAL CachedAt so a near-expired
		// remote entry does not get its TTL clock reset locally (else it could
		// live ~2×TTL). Fall back to the plain Put path when either concrete type
		// is not the one that supports timestamp-preserving writes.
		if rc, ok := t.remote.(*remoteCache); ok {
			if r, cachedAt, ok := rc.getWithCachedAt(key); ok {
				if dc, ok := t.local.(*diskCache); ok {
					_ = dc.putWithCachedAt(key, r, cachedAt) // best-effort, preserves TTL clock
				} else {
					_ = t.local.Put(key, r) // nop L1 or other backend: best-effort
				}
				return r, true, nil
			}
			return llm.Response{}, false, nil
		}
		// Non-*remoteCache remote (test stub): keep the original best-effort behavior.
		if r, ok, _ := t.remote.Get(key); ok {
			_ = t.local.Put(key, r)
			return r, true, nil
		}
	}
	return llm.Response{}, false, nil
}

func (t tiered) Put(key string, resp llm.Response) error {
	// L1 (disk) is the primary path — surface its error so disk-only and tiered
	// modes behave symmetrically (storeCache logs a non-nil Put error). L2
	// (remote) stays best-effort: it has its own internal warn and must never
	// fail a review over a lost shared-cache write.
	err := t.local.Put(key, resp)
	if t.remote != nil {
		_ = t.remote.Put(key, resp)
	}
	return err
}

// Options configures Open. The zero RemoteURL keeps today's disk-only
// behavior; everything remote is strictly opt-in.
type Options struct {
	TTL           time.Duration
	RemoteURL     string        // "" disables the remote tier
	RemoteToken   string        // resolved secret; "" = no auth header
	RemoteTimeout time.Duration // per-request HTTP timeout for the remote tier
}

// Open returns a Cache honoring Options. Disk is always attempted as L1; if
// the user cache dir can't be resolved it degrades to a nop L1 (warn). Remote
// is added as L2 only when RemoteURL is set; a bad RemoteURL warns and is
// dropped (never fatal). Always returns a usable Cache, never nil.
func Open(opts Options) Cache {
	var local Cache
	if d, err := New(opts.TTL); err == nil {
		local = d
	} else {
		slog.Warn("llm disk cache unavailable; continuing without a local cache tier", "err", err)
		local = nopCache{}
	}
	if opts.RemoteURL == "" {
		return local
	}
	r, err := newRemote(opts.RemoteURL, opts.RemoteToken, opts.TTL, opts.RemoteTimeout)
	if err != nil {
		slog.Warn("remote llm cache misconfigured; using local cache only", "err", err)
		return local
	}
	return tiered{local: local, remote: r}
}
