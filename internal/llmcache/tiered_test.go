package llmcache

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/llm"
)

// memCache is a tiny in-memory Cache stub for tiered tests — no HTTP needed.
type memCache struct {
	m map[string]llm.Response
}

func newMemCache() *memCache {
	return &memCache{m: map[string]llm.Response{}}
}

func (c *memCache) Get(key string) (llm.Response, bool, error) {
	r, ok := c.m[key]
	return r, ok, nil
}

func (c *memCache) Put(key string, resp llm.Response) error {
	c.m[key] = resp
	return nil
}

// mustNotCall fails the test on any interaction — used to prove the L1
// short-circuit never touches the remote tier.
type mustNotCall struct {
	t *testing.T
}

func (c mustNotCall) Get(string) (llm.Response, bool, error) {
	c.t.Fatal("remote tier must not be consulted on a local hit")
	return llm.Response{}, false, nil
}

func (c mustNotCall) Put(string, llm.Response) error {
	c.t.Fatal("remote tier must not be consulted on a local hit")
	return nil
}

func TestTieredLocalHitSkipsRemote(t *testing.T) {
	key := testKey()
	want := sampleResponse()

	local := newMemCache()
	if err := local.Put(key, want); err != nil {
		t.Fatalf("seed local: %v", err)
	}
	tc := tiered{local: local, remote: mustNotCall{t: t}}

	got, ok, err := tc.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected local hit")
	}
	if got != want {
		t.Fatalf("mismatch: got %+v want %+v", got, want)
	}
}

func TestTieredRemoteHitBackfillsLocal(t *testing.T) {
	key := testKey()
	want := sampleResponse()

	local := newMemCache()
	remote := newMemCache()
	if err := remote.Put(key, want); err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	tc := tiered{local: local, remote: remote}

	got, ok, err := tc.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected remote hit")
	}
	if got != want {
		t.Fatalf("mismatch: got %+v want %+v", got, want)
	}

	// The remote hit must have backfilled L1: a direct local read now hits.
	if r, ok, err := local.Get(key); err != nil || !ok || r != want {
		t.Fatalf("expected L1 backfill after remote hit, got ok=%v err=%v resp=%+v", ok, err, r)
	}
}

func TestTieredPutWritesBoth(t *testing.T) {
	key := testKey()
	want := sampleResponse()

	local := newMemCache()
	remote := newMemCache()
	tc := tiered{local: local, remote: remote}

	if err := tc.Put(key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if r, ok, _ := local.Get(key); !ok || r != want {
		t.Fatalf("local after Put: ok=%v resp=%+v, want the stored response", ok, r)
	}
	if r, ok, _ := remote.Get(key); !ok || r != want {
		t.Fatalf("remote after Put: ok=%v resp=%+v, want the stored response", ok, r)
	}
}

func TestTieredRemoteNilIsLocalOnly(t *testing.T) {
	key := testKey()
	want := sampleResponse()

	local := newMemCache()
	tc := tiered{local: local, remote: nil}

	if _, ok, err := tc.Get(key); ok || err != nil {
		t.Fatalf("expected miss on empty local-only tiered, got ok=%v err=%v", ok, err)
	}
	if err := tc.Put(key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := tc.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected hit after Put in local-only mode")
	}
	if got != want {
		t.Fatalf("mismatch: got %+v want %+v", got, want)
	}
}

// errPutCache is a Cache stub whose Get always misses and whose Put always
// fails with a fixed error — used to prove which tier's Put error tiered
// surfaces vs swallows.
type errPutCache struct {
	err error
}

func (c errPutCache) Get(string) (llm.Response, bool, error) {
	return llm.Response{}, false, nil
}

func (c errPutCache) Put(string, llm.Response) error {
	return c.err
}

// TestTieredPutSurfacesLocalError: the L1 (disk) Put error must surface to the
// caller so disk-only and tiered modes behave symmetrically (storeCache
// debug-logs it), while the remote tier must still receive the write.
func TestTieredPutSurfacesLocalError(t *testing.T) {
	key := testKey()
	want := sampleResponse()
	localErr := errors.New("disk full")

	remote := newMemCache()
	tc := tiered{local: errPutCache{err: localErr}, remote: remote}

	if err := tc.Put(key, want); !errors.Is(err, localErr) {
		t.Fatalf("Put error = %v, want the local error %v", err, localErr)
	}
	if r, ok, _ := remote.Get(key); !ok || r != want {
		t.Fatalf("remote must still receive the write despite the local error: ok=%v resp=%+v", ok, r)
	}
}

// TestTieredPutRemoteErrorIgnored: the remote (L2) tier is best-effort — its
// Put error must never surface, and the local write must succeed normally.
func TestTieredPutRemoteErrorIgnored(t *testing.T) {
	key := testKey()
	want := sampleResponse()

	local := newMemCache()
	tc := tiered{local: local, remote: errPutCache{err: errors.New("remote down")}}

	if err := tc.Put(key, want); err != nil {
		t.Fatalf("Put must ignore the remote error, got %v", err)
	}
	if r, ok, _ := local.Get(key); !ok || r != want {
		t.Fatalf("local after Put: ok=%v resp=%+v, want the stored response", ok, r)
	}
}

// TestTieredBackfillPreservesCachedAt: when a remote hit backfills L1, the
// local entry must carry the remote entry's ORIGINAL CachedAt — a fresh Put
// would reset the TTL clock and let a near-expired remote entry live ~2×TTL
// locally. Uses the real *diskCache and *remoteCache so the type-asserted
// timestamp-preserving path is the one under test.
func TestTieredBackfillPreservesCachedAt(t *testing.T) {
	const ttl = 24 * time.Hour
	key := testKey()
	want := sampleResponse()

	// Seed the remote KV store with an entry cached 23h ago — one hour from
	// expiry under the 24h TTL.
	origCachedAt := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Second)
	data, err := json.Marshal(wireResponse{
		CachedAt: origCachedAt,
		Content:  want.Content,
		Risk: wireRisk{
			Level:     want.Risk.Level,
			Summary:   want.Risk.Summary,
			Heuristic: want.Risk.Heuristic,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv, store := newKVServer(t)
	store.Store("/"+key, data)

	local := NewInDir(t.TempDir(), ttl)
	remote := newTestRemote(t, srv.URL, "", ttl)
	tc := tiered{local: local, remote: remote}

	got, ok, err := tc.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected remote hit")
	}
	if got != want {
		t.Fatalf("mismatch: got %+v want %+v", got, want)
	}

	// Read the backfilled L1 file directly: its CachedAt must be the remote
	// entry's original timestamp, not a fresh time.Now().
	p, err := local.path(key)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read backfilled L1 entry: %v", err)
	}
	var w wireResponse
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("parse backfilled L1 entry: %v", err)
	}
	if !w.CachedAt.Equal(origCachedAt) {
		t.Errorf("backfilled CachedAt = %v, want the original remote %v (TTL clock must not reset)", w.CachedAt, origCachedAt)
	}
	if time.Since(w.CachedAt) < 22*time.Hour {
		t.Errorf("backfilled CachedAt %v is too recent — looks like a fresh Put reset the TTL clock", w.CachedAt)
	}
}
