package llmcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akomyagin/gitl/internal/llm"
)

// diskCache stores LLM responses on disk under dir, expiring entries older
// than ttl. It is the L1 tier; the store is safe for concurrent writers (temp
// file + atomic rename). External packages only ever see it through the Cache
// interface.
type diskCache struct {
	dir string
	ttl time.Duration
}

// New creates a diskCache under os.UserCacheDir()/gitl/review. It returns an
// error when the user cache dir cannot be resolved; callers degrade to "no
// cache" rather than crashing.
func New(ttl time.Duration) (*diskCache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache dir: %w", err)
	}
	return &diskCache{dir: filepath.Join(base, "gitl", "review"), ttl: ttl}, nil
}

// NewInDir creates a diskCache rooted at an explicit directory (for tests).
func NewInDir(dir string, ttl time.Duration) *diskCache {
	return &diskCache{dir: dir, ttl: ttl}
}

// KeyParams enumerates every request parameter that must contribute to the
// cache key: two requests differing in ANY of these fields must never collide
// on the same key (a base_url or deployment change targets a different
// backend; temperature/max_tokens change the response itself). BaseURL is the
// RESOLVED endpoint (config.Load fills provider defaults in before call sites
// build a key). The key scheme is backend-agnostic: disk and remote share it.
type KeyParams struct {
	Provider        string
	Model           string
	BaseURL         string
	AzureEndpoint   string
	AzureDeployment string
	AzureAPIVersion string
	System          string
	User            string
	Temperature     float64
	MaxTokens       int
}

// Key hashes all request parameters into a hex SHA-256 key. Fields are joined
// with a NUL separator, which is not a valid byte in any provider/model/
// endpoint/version identifier — those fields can never be confused by
// concatenation. System/User embed diff text (git diff runs without
// --text), so a NUL is not strictly impossible there; the worst case is a
// spurious cache hit/miss within the user's own repo, not a cross-boundary
// collision.
func Key(p KeyParams) string {
	fields := []string{
		p.Provider,
		p.Model,
		p.BaseURL,
		p.AzureEndpoint,
		p.AzureDeployment,
		p.AzureAPIVersion,
		strconv.FormatFloat(p.Temperature, 'g', -1, 64),
		strconv.Itoa(p.MaxTokens),
		p.System,
		p.User,
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// keyShardLen is the length of the hex prefix used to shard cache entries
// across subdirectories, keeping any single directory from growing too large.
const keyShardLen = 2

// shard returns the first keyShardLen characters of key, or an error if key
// is shorter than that. Key() always returns a 64-char hex SHA-256, so this
// is unreachable via normal use — a defensive guard against a malformed key
// reaching path()/Put() some other way.
func shard(key string) (string, error) {
	if len(key) < keyShardLen {
		return "", fmt.Errorf("cache key %q too short to shard (need >= %d chars)", key, keyShardLen)
	}
	return key[:keyShardLen], nil
}

// path returns the sharded on-disk path for a key.
func (c *diskCache) path(key string) (string, error) {
	s, err := shard(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.dir, s, key+".json"), nil
}

// Get returns the cached response for key. It reports (zero, false, nil) on a
// miss (including an expired entry, which it best-effort deletes) and
// (zero, false, err) when an existing entry exists but cannot be read or parsed.
func (c *diskCache) Get(key string) (llm.Response, bool, error) {
	p, err := c.path(key)
	if err != nil {
		return llm.Response{}, false, err
	}
	data, err := os.ReadFile(p) //nolint:gosec // path derived from a hashed key, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return llm.Response{}, false, nil
		}
		return llm.Response{}, false, fmt.Errorf("read cache %q: %w", p, err)
	}

	resp, ok, err := decodeResponse(data, c.ttl)
	if err != nil {
		// A corrupt/unreadable entry would otherwise be re-read (and re-fail) on
		// every review with this key, silently forcing a full LLM call each time
		// and never self-healing. Evict it best-effort and warn (mirrors remote.go's
		// warn-on-parse-failure), so the NEXT review re-populates a clean entry.
		_ = os.Remove(p)
		slog.Warn("evicting corrupt llm cache entry", "path", p, "err", err)
		return llm.Response{}, false, fmt.Errorf("cache %q: %w", p, err)
	}
	if !ok {
		_ = os.Remove(p) // best-effort eviction of an expired entry
		return llm.Response{}, false, nil
	}
	return resp, true, nil
}

// Put atomically writes resp with CachedAt = now. It delegates to
// putWithCachedAt; the split exists so tiered.Get can backfill L1 with the
// original remote CachedAt (preserving the TTL clock) via the same atomic
// write path without going through the public Put (which would reset the clock).
func (c *diskCache) Put(key string, resp llm.Response) error {
	return c.putWithCachedAt(key, resp, time.Now())
}

// putWithCachedAt atomically writes resp to the sharded path for key with an
// explicit CachedAt. It creates the shard directory, writes to a uniquely-named
// temp file in the same directory, then renames it into place so concurrent
// writers never observe a partial file.
func (c *diskCache) putWithCachedAt(key string, resp llm.Response, cachedAt time.Time) error {
	s, err := shard(key)
	if err != nil {
		return err
	}
	subdir := filepath.Join(c.dir, s)
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		return fmt.Errorf("create cache dir %q: %w", subdir, err)
	}

	data, err := encodeResponseAt(resp, cachedAt)
	if err != nil {
		return err
	}

	tmp := filepath.Join(subdir, fmt.Sprintf("%s.json.tmp.%x", key, rand.Int63())) //nolint:gosec // tmp suffix only needs uniqueness, not crypto strength
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write cache temp %q: %w", tmp, err)
	}

	dst := filepath.Join(subdir, key+".json")
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename cache temp into place %q: %w", dst, err)
	}
	return nil
}
