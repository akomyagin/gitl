package cli

import (
	"strings"
	"testing"
)

// TestReviewDoesNotLeakAPIKeyInVerboseOutput is the Этап 4 secrets test
// (docs/TECHNICAL_PLAN.md §12.5). It differs from the existing Этап 2 test
// (internal/llm/client_test.go TestClientCompleteErrorDoesNotLeakAPIKey),
// which only checks the LLM client's own error string: here the whole
// `review` pipeline runs under --verbose (which raises slog to debug level
// and is exactly the mode a GitHub Action step would use for diagnostics),
// and BOTH stdout and stderr are captured — catching a leak introduced
// anywhere in the CLI layer (debug logging, error wrapping, rendering), not
// just inside the LLM client.
//
// The error path is deliberately forced: pointing --base-url at a host that
// refuses the connection makes Complete return a wrapped network error
// (see runReview's `fmt.Errorf("review failed: %w", err)`), which is exactly
// the kind of place a careless %v/%+v could accidentally embed the request
// (and thus the Authorization header/API key) in an error message. A happy
// path would never exercise that wrapping at all.
func TestReviewDoesNotLeakAPIKeyInVerboseOutput(t *testing.T) {
	dir := setupRepo(t, false)
	const secret = "sk-super-secret-marker-4711"

	env := map[string]string{"GITL_API_KEY": secret}
	osStderr := captureStderr(t, func() {
		out, err := runReviewInDir(t, dir, env, "HEAD~1..HEAD",
			"--verbose",
			"--base-url=http://127.0.0.1:1", // reserved/unroutable port: connection refused, forces the online error path
		)
		if err == nil {
			t.Fatal("expected the review to fail against an unroutable base-url")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("returned error leaks the API key: %v", err)
		}
		if strings.Contains(out, secret) {
			t.Errorf("stdout leaks the API key:\n%s", out)
		}
	})
	if strings.Contains(osStderr, secret) {
		t.Errorf("stderr (verbose/slog output) leaks the API key:\n%s", osStderr)
	}
}
