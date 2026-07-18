package llm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// doHTTPRoundTrip executes a single already-built HTTP request and returns
// the raw response body on HTTP 200. It is the transport tail shared by every
// provider's doOnce: transport errors are wrapped as retryable networkError
// (unless the context is done), the body is read under maxResponseBytes, and
// non-200 statuses are classified via classifyStatus with the provider's own
// error-message extractor (parseErr, never nil).
//
// redactErr is nil-safe and applied ONLY to the transport error, BEFORE it is
// wrapped in networkError — so a provider whose URL carries a secret (Gemini's
// ?key=) can redact *url.Error text while keeping the networkError type that
// isRetryable relies on. openai/anthropic pass nil (no-op).
func doHTTPRoundTrip(
	ctx context.Context,
	httpReq *http.Request,
	httpClient *http.Client,
	provider string,
	parseErr func([]byte) string,
	redactErr func(error) error,
) ([]byte, error) {
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		// Network/transport errors are retryable (unless the context is done).
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm: request cancelled: %w", ctx.Err())
		}
		if redactErr != nil {
			err = redactErr(err)
		}
		return nil, &networkError{err: err}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyStatus(provider, resp.StatusCode, parseErr(respBody))
	}

	return respBody, nil
}

// finishWithRisk applies the shared risk-block contract (§7.2, §7.3) to a
// model reply: parse the trailing risk block, or fall back to the
// deterministic heuristic (with a warning) when it is missing or invalid.
// It is the common tail of every provider's Complete method.
func finishWithRisk(content string, commits []gitlog.Commit, diff string) Response {
	stripped, risk, ok := ParseRisk(content)
	if !ok {
		risk = HeuristicRisk(commits, diff)
		risk.Heuristic = true
		slog.Warn("model risk block missing or invalid; using heuristic fallback", "level", risk.Level)
		stripped = strings.TrimRight(content, " \t\r\n") + "\n"
	}
	return Response{Content: stripped, Risk: risk}
}
