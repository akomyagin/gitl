// Package riskhistory is a local, append-only JSONL log of review risk
// outcomes, powering the "Risk trend" section of `gitl digest`. It is a
// best-effort history, not a source of truth: writers never fail a review over
// it, readers skip anything they cannot parse, and the whole feature is
// machine-local by design (a CI runner's cold disk never accumulates history —
// documented in the README, not silently broken). Only the standard library
// plus internal/gitlog (for the repo identity key) are used; deliberately no
// store interface, no pluggable backends, no retention policy (v1
// anti-over-engineering guard).
package riskhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// RecordSchemaVersion is the wire schema version written into every record.
// Readers skip records with a NEWER version (written by a future gitl) instead
// of misinterpreting them.
const RecordSchemaVersion = 1

// Record is one review outcome as stored on disk (one JSON object per line).
type Record struct {
	SchemaVersion int       `json:"schema_version"` // = RecordSchemaVersion
	Timestamp     time.Time `json:"timestamp"`      // UTC, time.Now().UTC()
	Repo          string    `json:"repo"`           // repo identity key — see RepoKey
	Range         string    `json:"range"`          // diffSource label: "HEAD~5..HEAD", "pr/42", "staged"
	RiskLevel     string    `json:"risk_level"`     // "low" | "medium" | "high"
	RiskSummary   string    `json:"risk_summary"`
	Provider      string    `json:"provider"` // cfg.LLM.Provider
	Model         string    `json:"model"`    // cfg.LLM.Model
	Heuristic     bool      `json:"heuristic"`
	Offline       bool      `json:"offline"`
}

// historyFileName is the JSONL log file name under the gitl data dir.
const historyFileName = "risk-history.jsonl"

// Log is a handle to the on-disk risk-history JSONL file.
type Log struct {
	path string
}

// New creates a Log at the real per-user data path (see dataDir). It returns
// an error when no data dir can be resolved; callers degrade to "no history
// logging" rather than crashing — the same pattern as llmcache.New.
func New() (*Log, error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, historyFileName)}, nil
}

// NewInDir creates a Log rooted at an explicit directory (for tests) —
// mirrors llmcache.NewInDir.
func NewInDir(dir string) *Log {
	return &Log{path: filepath.Join(dir, historyFileName)}
}

// dataDir resolves the gitl data directory. The log is meaningful history,
// not an ephemeral cache — a user must not lose it to `rm -rf ~/.cache` — so
// it lives in the XDG DATA dir, not the cache dir. Go's stdlib has no
// os.UserDataDir(), hence this resolver:
//
//  1. $XDG_DATA_HOME (set and absolute, any OS) → $XDG_DATA_HOME/gitl.
//  2. Windows: os.UserConfigDir() (%AppData%) + gitl — there is no separate
//     data-dir convention there, consistent with config.go's use of
//     os.UserConfigDir().
//  3. Otherwise: ~/.local/share/gitl from os.UserHomeDir().
func dataDir() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "gitl"), nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		return filepath.Join(dir, "gitl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "gitl"), nil
}

// RepoKey computes the stable identity used to correlate review records with a
// repository. It prefers the origin remote URL, canonicalized via
// gitlog.ParseRemoteURL into "host/owner/repo" (lowercase, scheme-free,
// .git-free) — identical for the ssh and https forms of the same repository —
// and falls back to the absolute worktree root when there is no origin remote.
// Never returns an error — on total failure it returns "" and the caller
// writes/filters on the empty key (records still logged, just not
// correlatable; acceptable for v1).
func RepoKey(ctx context.Context, runner *gitlog.Runner) string {
	if runner == nil {
		return ""
	}
	if url, err := runner.RemoteURL(ctx, "origin"); err == nil {
		if r, ok := gitlog.ParseRemoteURL(url); ok {
			return strings.ToLower(r.Host) + "/" + strings.ToLower(r.Owner) + "/" + strings.ToLower(r.Repo)
		}
		// Unparsed remote (local path, exotic scheme): fall back to the trimmed
		// raw URL so a key is still produced, rather than dropping to worktree root.
		if key := strings.TrimSuffix(strings.TrimSpace(url), ".git"); key != "" {
			return key
		}
	}
	if root, err := runner.TopLevel(ctx); err == nil {
		return root
	}
	return ""
}

// Append writes one record as a single JSON line, creating the data dir and
// file if needed. Uses O_APPEND|O_CREATE|O_WRONLY with 0o600. Concurrent
// single-line appends under a page-size threshold are atomic on POSIX with
// O_APPEND; good enough for a local dev tool — deliberately no flock.
func (l *Log) Append(rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal risk-history record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("create data dir %q: %w", filepath.Dir(l.path), err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // fixed path under the user data dir, not user input
	if err != nil {
		return fmt.Errorf("open risk-history log %q: %w", l.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append risk-history record: %w", err)
	}
	return nil
}

// Load reads all records, skipping malformed and newer-schema lines
// (slog.Debug — the log is best-effort history, not a source of truth),
// returning them in file order (chronological). A missing file returns
// (nil, nil) — never an error.
func (l *Log) Load() ([]Record, error) {
	data, err := os.ReadFile(l.path) //nolint:gosec // fixed path under the user data dir, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read risk-history log %q: %w", l.path, err)
	}
	var records []Record
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			slog.Debug("skipping malformed risk-history line", "err", err)
			continue
		}
		if rec.SchemaVersion > RecordSchemaVersion {
			slog.Debug("skipping newer-schema risk-history record", "schema_version", rec.SchemaVersion)
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// recentListLimit caps the Trend.Recent short list.
const recentListLimit = 5

// Trend is the minimal deterministic aggregation over one repository's
// records within a day window: level distribution, a crude-but-honest
// direction signal (high-risk count in the recent half vs the earlier half),
// and a short newest-first list. Deliberately no percentages or fancier
// statistics (v1).
type Trend struct {
	Days         int
	RepoKey      string
	Total        int
	Counts       map[string]int // "low"/"medium"/"high" -> count over the whole window
	RecentHigh   int            // high-risk count in the more recent half of the window
	PreviousHigh int            // high-risk count in the older half
	Recent       []Record       // up to recentListLimit latest records, newest first
}

// Aggregate filters records to repoKey and to [now-days, now], then computes
// the Trend. now is a parameter (not time.Now()) for testability. An empty
// window still returns a valid Trend{Days, RepoKey, Counts: empty map} — the
// caller drops the section when Total == 0.
func Aggregate(records []Record, repoKey string, days int, now time.Time) Trend {
	trend := Trend{Days: days, RepoKey: repoKey, Counts: map[string]int{}}
	cutoff := now.AddDate(0, 0, -days)
	midpoint := cutoff.Add(now.Sub(cutoff) / 2)

	var matched []Record
	for _, rec := range records {
		if rec.Repo != repoKey {
			continue
		}
		if rec.Timestamp.Before(cutoff) || rec.Timestamp.After(now) {
			continue
		}
		matched = append(matched, rec)
		trend.Total++
		trend.Counts[rec.RiskLevel]++
		if rec.RiskLevel == "high" {
			if rec.Timestamp.Before(midpoint) {
				trend.PreviousHigh++
			} else {
				trend.RecentHigh++
			}
		}
	}

	// Newest first: file order is chronological, so sort by timestamp
	// descending (stable, so same-timestamp records keep their relative file
	// order — deterministic) and take the head.
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Timestamp.After(matched[j].Timestamp)
	})
	if len(matched) > recentListLimit {
		matched = matched[:recentListLimit]
	}
	trend.Recent = matched
	return trend
}
