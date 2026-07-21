package llmcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/llm"
)

func sampleResponse() llm.Response {
	return llm.Response{
		Content: "review body",
		Risk: llm.Risk{
			Level:     "medium",
			Summary:   "some risk",
			Heuristic: true,
		},
	}
}

func TestMissPutHit(t *testing.T) {
	c := NewInDir(t.TempDir(), 24*time.Hour)
	key := Key("openai", "gpt-4o", "sys", "usr")

	if _, ok, err := c.Get(key); ok || err != nil {
		t.Fatalf("expected miss, got ok=%v err=%v", ok, err)
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

func TestExpiry(t *testing.T) {
	c := NewInDir(t.TempDir(), 24*time.Hour)
	key := Key("openai", "gpt-4o", "sys", "usr")
	if err := c.Put(key, sampleResponse()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Overwrite the entry with a CachedAt beyond the 24h TTL.
	stale := wireResponse{
		CachedAt: time.Now().Add(-48 * time.Hour),
		Content:  "old",
		Risk:     wireRisk{Level: "low"},
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p, err := c.path(key)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	got, ok, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatalf("expected expired miss, got hit %+v", got)
	}
}

func TestConcurrentPut(t *testing.T) {
	c := NewInDir(t.TempDir(), 24*time.Hour)
	key := Key("openai", "gpt-4o", "sys", "usr")
	resp := sampleResponse()

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := c.Put(key, resp); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
	}
	wg.Wait()

	got, ok, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected hit after concurrent Put")
	}
	if got != resp {
		t.Fatalf("corrupted entry: got %+v want %+v", got, resp)
	}
}

func TestKeyDifferentProviders(t *testing.T) {
	s, u := "sys", "usr"
	if Key("openai", "gpt-4o", s, u) == Key("ollama", "llama3", s, u) {
		t.Fatal("keys must differ across providers")
	}
}

func TestKeyDifferentModels(t *testing.T) {
	s, u := "sys", "usr"
	if Key("openai", "gpt-4o", s, u) == Key("openai", "gpt-4o-mini", s, u) {
		t.Fatal("keys must differ across models")
	}
}

// TestShortKeyReturnsError guards the shard() length check: a key shorter than
// keyShardLen must produce a clear error from Get/Put, not an
// out-of-range panic on key[:2]. Key() never produces such a key, so this is
// purely the defensive path.
func TestShortKeyReturnsError(t *testing.T) {
	c := NewInDir(t.TempDir(), time.Hour)

	for _, key := range []string{"", "a"} {
		if _, ok, err := c.Get(key); err == nil || ok {
			t.Errorf("Get(%q): expected error, got ok=%v err=%v", key, ok, err)
		}
		if err := c.Put(key, sampleResponse()); err == nil {
			t.Errorf("Put(%q): expected error, got nil", key)
		}
	}
}

func TestNewCacheDirCreated(t *testing.T) {
	c := NewInDir(filepath.Join(t.TempDir(), "sub", "dir"), time.Hour)
	key := Key("openai", "gpt-4o", "sys", "usr")

	if err := c.Put(key, sampleResponse()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok, err := c.Get(key); err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
}
