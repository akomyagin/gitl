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

// Compile-time proof that every backend satisfies the Cache contract.
var (
	_ Cache = (*diskCache)(nil)
	_ Cache = (*remoteCache)(nil)
	_ Cache = nopCache{}
	_ Cache = tiered{}
)

// testKey is the baseline key used by the store (Get/Put) tests.
func testKey() string {
	return Key(KeyParams{Provider: "openai", Model: "gpt-4o", System: "sys", User: "usr"})
}

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
	key := testKey()

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
	key := testKey()
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
	key := testKey()
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

// TestKeyFieldSensitivity: changing ANY single KeyParams field must change the
// key (a collision would silently serve the wrong provider's/endpoint's/
// parameters' cached response), while identical params must be stable.
func TestKeyFieldSensitivity(t *testing.T) {
	base := KeyParams{
		Provider:        "openai",
		Model:           "gpt-4o",
		BaseURL:         "https://api.openai.com/v1",
		AzureEndpoint:   "https://acct.openai.azure.com",
		AzureDeployment: "prod-gpt4o",
		AzureAPIVersion: "2024-06-01",
		System:          "sys",
		User:            "usr",
		Temperature:     0.2,
		MaxTokens:       1500,
	}

	tests := []struct {
		name   string
		mutate func(p *KeyParams)
	}{
		{"provider", func(p *KeyParams) { p.Provider = "ollama" }},
		{"model", func(p *KeyParams) { p.Model = "gpt-4o-mini" }},
		{"base_url", func(p *KeyParams) { p.BaseURL = "http://localhost:11434/v1" }},
		{"azure_endpoint", func(p *KeyParams) { p.AzureEndpoint = "https://other.openai.azure.com" }},
		{"azure_deployment", func(p *KeyParams) { p.AzureDeployment = "staging-gpt4o" }},
		{"azure_api_version", func(p *KeyParams) { p.AzureAPIVersion = "2024-10-01" }},
		{"system", func(p *KeyParams) { p.System = "sys2" }},
		{"user", func(p *KeyParams) { p.User = "usr2" }},
		{"temperature", func(p *KeyParams) { p.Temperature = 0.7 }},
		{"max_tokens", func(p *KeyParams) { p.MaxTokens = 2000 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := base
			tt.mutate(&mutated)
			if Key(mutated) == Key(base) {
				t.Errorf("keys must differ when only %s differs", tt.name)
			}
		})
	}

	if Key(base) != Key(base) {
		t.Error("identical params must produce identical keys")
	}
}

// TestKeyFieldsUnambiguous guards the NUL-separator join: shifting content
// across a field boundary must not produce the same key (naive concatenation
// would make {"ab","c"} collide with {"a","bc"}).
func TestKeyFieldsUnambiguous(t *testing.T) {
	a := KeyParams{Provider: "ab", Model: "c"}
	b := KeyParams{Provider: "a", Model: "bc"}
	if Key(a) == Key(b) {
		t.Error("field boundaries must be unambiguous in the hashed canonical form")
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
	key := testKey()

	if err := c.Put(key, sampleResponse()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok, err := c.Get(key); err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
}
