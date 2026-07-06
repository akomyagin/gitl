package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update regenerates golden files instead of comparing against them (§11.2):
//
//	go test ./internal/render/... -update
var update = flag.Bool("update", false, "update golden files in testdata/")

// assertGolden compares got against the golden file at path (relative to
// this package, e.g. "testdata/changelog/basic.md"). With -update it writes
// got as the new golden content instead of comparing. Comparison is
// byte-for-byte (§11.2 / SKILL.md §4).
func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for golden %s: %v", path, err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path) //nolint:gosec // test-only, path is a hardcoded testdata literal
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./internal/render/... -update` to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
