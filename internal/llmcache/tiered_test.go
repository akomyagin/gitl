package llmcache

import (
	"testing"

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
