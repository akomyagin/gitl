package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Streamer is an optional interface for providers that support token-by-token
// SSE streaming. Client implements it; Offline does not.
type Streamer interface {
	Stream(ctx context.Context, req Request, w io.Writer) (Response, error)
}

var _ Streamer = (*Client)(nil)

// streamChatRequest is the streaming wire format. It mirrors chatRequest but
// adds the "stream" flag; it is kept separate so the non-streaming path is
// unaffected.
type streamChatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

// streamChunk is one SSE "data:" event of an OpenAI-compatible streaming
// response. Only the incremental delta content is consumed.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// sanitizingWriter strips terminal control characters from everything written
// through it, defusing ANSI/terminal escape injection in streamed model output
// (which may quote attacker-controlled commit messages verbatim). The policy
// mirrors render's sanitizeTerminal — deliberately duplicated here, not
// imported, to avoid a render↔llm cross-package dependency: valid runes in C0
// (0x00–0x1F, except '\t' and '\n'), DEL (0x7F), and C1 (0x80–0x9F) are
// removed (not replaced). Bytes that do not decode as valid UTF-8 (e.g. a
// multi-byte rune split across an SSE chunk boundary) pass through untouched —
// only validly-decoded control runes are dropped.
type sanitizingWriter struct{ w io.Writer }

func (s sanitizingWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid/truncated byte (possibly a rune cut by a chunk boundary):
			// keep it as-is rather than risk corrupting multi-byte content.
			out = append(out, p[i])
			i++
			continue
		}
		if dropControlRune(r) {
			i += size
			continue
		}
		out = append(out, p[i:i+size]...)
		i += size
	}
	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	// The whole input is considered consumed even when bytes were filtered
	// out, per the io.Writer contract (no spurious short-write errors).
	return len(p), nil
}

// dropControlRune reports whether r is a terminal control rune to remove:
// C0 minus '\t'/'\n', DEL, or C1.
func dropControlRune(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	return r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F)
}

// Stream sends a streaming chat/completions request and writes each token to w
// as it arrives, accumulating the full text so the risk block can be parsed
// after the stream completes. Unlike Complete, it performs NO retry: once any
// byte of the body is read the request cannot be safely replayed. A non-200
// status returned before the first byte is surfaced as a *StatusError so the
// caller may fall back to the non-streaming Complete path.
func (c *Client) Stream(ctx context.Context, req Request, w io.Writer) (Response, error) {
	messages := make([]chatMessage, 0, 2)
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.User})

	body, err := json.Marshal(streamChatRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
	})
	if err != nil {
		return Response{}, fmt.Errorf("llm: marshal stream request: %w", err)
	}

	httpReq, err := c.buildRequest(ctx, body)
	if err != nil {
		return Response{}, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("llm: request cancelled: %w", ctx.Err())
		}
		return Response{}, &networkError{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return Response{}, classifyStatus(c.provider, resp.StatusCode, extractErrorMessage(msg))
	}

	// Terminal-facing writer: strip control characters as tokens arrive. buf
	// below intentionally keeps the RAW text — ParseRisk needs the unfiltered
	// stream, and the risk header is sanitized on output via
	// render.RiskHeaderLine.
	sw := sanitizingWriter{w: w}

	var buf strings.Builder
	// Cap the TOTAL bytes read from the streaming body at the same threshold
	// the non-streaming path applies (maxResponseBytes): a misbehaving or
	// malicious endpoint (e.g. a self-hosted base_url) could otherwise stream
	// data: chunks forever and grow buf without bound. When the limit is hit
	// the reader reports EOF and the loop ends normally — the same silent
	// truncation pattern doHTTPRoundTrip already uses; ParseRisk falls back to
	// the heuristic risk if the tail was cut off.
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxResponseBytes))
	// SSE events can carry lines well beyond the default 64 KiB scanner limit;
	// expand to 1 MiB per line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Skip malformed chunks rather than aborting the whole stream.
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		if _, err := io.WriteString(sw, delta); err != nil {
			return Response{}, fmt.Errorf("llm: write stream output: %w", err)
		}
		buf.WriteString(delta)
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("llm: read stream: %w", err)
	}

	stripped, risk, ok := ParseRisk(buf.String())
	if !ok {
		risk = HeuristicRisk(req.Commits, req.Diff)
		risk.Heuristic = true
		slog.Warn("model risk block missing or invalid; using heuristic fallback", "level", risk.Level)
		stripped = strings.TrimRight(buf.String(), " \t\r\n") + "\n"
	}
	return Response{Content: stripped, Risk: risk}, nil
}
