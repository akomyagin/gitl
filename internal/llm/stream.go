package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/akomyagin/gitl/internal/textsafe"
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
// response. The incremental delta content is consumed; a top-level "error"
// object (how OpenAI-compatible proxies report in-stream failures over a 200
// response) aborts the stream instead of being skipped as a zero-Choices chunk.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// sanitizingWriter strips terminal control characters from everything written
// through it, defusing ANSI/terminal escape injection and bidi "Trojan Source"
// spoofing in streamed model output (which may quote attacker-controlled
// commit messages verbatim). The removal policy is the shared
// textsafe.DropRune set — the same one render's sanitizeTerminal applies on
// the buffered path — so the two output paths cannot diverge.
//
// The writer is stateful across Write calls: SSE delivers text in arbitrary
// chunk-sized pieces, so a multi-byte UTF-8 rune (including the encoding of a
// control character such as U+009B CSI, bytes 0xC2 0x9B) can be split exactly
// at a chunk boundary. A trailing incomplete-but-potentially-valid sequence is
// held in pending and re-examined when the next chunk arrives, instead of
// leaking through one byte at a time past the rune filter. Bytes that are
// definitively invalid UTF-8 (not merely incomplete) pass through untouched,
// as before. Use via pointer receiver; call flush at end of stream.
type sanitizingWriter struct {
	w io.Writer
	// pending holds at most utf8.UTFMax-1 bytes of a trailing incomplete
	// multi-byte sequence, carried over to the next Write call.
	pending []byte
}

func (s *sanitizingWriter) Write(p []byte) (int, error) {
	buf := p
	if len(s.pending) > 0 {
		// Prepend the held-over tail of the previous chunk. pending is at most
		// 3 bytes, so the copy is negligible.
		buf = append(s.pending, p...)
		s.pending = nil
	}
	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); {
		if !utf8.FullRune(buf[i:]) {
			// Incomplete prefix of a valid multi-byte sequence at the end of
			// the input (FullRune is false ONLY for such prefixes — invalid
			// encodings count as full width-1 error runes). Hold it for the
			// next Write instead of emitting the bytes unfiltered.
			s.pending = append([]byte(nil), buf[i:]...)
			break
		}
		r, size := utf8.DecodeRune(buf[i:])
		if r == utf8.RuneError && size == 1 {
			// Definitively invalid byte: keep it as-is rather than risk
			// corrupting surrounding content (matches the buffered path,
			// where a lone invalid byte renders as U+FFFD — harmless).
			out = append(out, buf[i])
			i++
			continue
		}
		if textsafe.DropRune(r) {
			i += size
			continue
		}
		out = append(out, buf[i:i+size]...)
		i += size
	}
	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	// The whole input is considered consumed even when bytes were filtered
	// out or held over in pending, per the io.Writer contract (no spurious
	// short-write errors).
	return len(p), nil
}

// flush emits any bytes still held in pending at the true end of the stream.
// A stream that legitimately ends mid-rune (e.g. LimitReader truncation) would
// otherwise silently lose those bytes; they pass through as-is, matching the
// invalid-byte policy above (an incomplete tail can no longer assemble into a
// control rune once no more input is coming).
func (s *sanitizingWriter) flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	p := s.pending
	s.pending = nil
	_, err := s.w.Write(p)
	return err
}

// riskBlockMarker is the exact byte sequence that opens the trailing risk
// block the model emits per the review prompt contract (a fenced code block
// tagged `risk`, the last thing in the reply). On the streaming path this
// block must be parsed (ParseRisk over the full buffer) but NOT shown to the
// user — the buffered path strips it via ParseRisk, and the two output paths
// must not diverge.
const riskBlockMarker = "```risk"

// riskBlockGate forwards streamed text to the terminal writer until it
// detects the start of the risk block, then suppresses everything from that
// point on. Because the risk block is, by prompt contract, the LAST thing the
// model outputs, suppression never needs to resume — a one-way latch is
// sufficient and robust.
//
// The marker may be split across SSE deltas, so up to len(marker)-1 trailing
// bytes are held back before being forwarded: they might turn out to be the
// prefix of the marker once the next delta arrives. On stream end (flush) any
// held-back tail that did NOT become the marker is emitted.
type riskBlockGate struct {
	w        *sanitizingWriter
	held     []byte
	suppress bool
}

func (g *riskBlockGate) write(p string) error {
	if g.suppress {
		return nil
	}
	buf := append(g.held, p...)
	g.held = nil

	if idx := bytes.Index(buf, []byte(riskBlockMarker)); idx >= 0 {
		g.suppress = true
		return g.emit(buf[:idx])
	}

	keep := longestMarkerPrefixSuffix(buf)
	forward := buf[:len(buf)-keep]
	g.held = append([]byte(nil), buf[len(buf)-keep:]...)
	return g.emit(forward)
}

func (g *riskBlockGate) emit(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	_, err := g.w.Write(b)
	return err
}

func (g *riskBlockGate) flush() error {
	if g.suppress || len(g.held) == 0 {
		g.held = nil
		return nil
	}
	tail := g.held
	g.held = nil
	return g.emit(tail)
}

// longestMarkerPrefixSuffix returns the length of the longest suffix of buf
// that is a prefix of riskBlockMarker. Those trailing bytes are held back
// because the next delta might complete the marker. Bounded by len(marker)-1.
func longestMarkerPrefixSuffix(buf []byte) int {
	m := []byte(riskBlockMarker)
	max := len(m) - 1
	if max > len(buf) {
		max = len(buf)
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(buf[len(buf)-n:], m[:n]) {
			return n
		}
	}
	return 0
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
	// render.RiskHeaderLine. The riskBlockGate additionally cuts the trailing
	// ```risk block out of the TERMINAL output only (never out of buf), so the
	// streaming path no longer diverges from the buffered path, which strips
	// that block via ParseRisk before rendering.
	sw := &sanitizingWriter{w: w}
	gate := &riskBlockGate{w: sw}

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
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		// SSE spec: a single leading space after the colon is stripped if
		// present, but is not required — "data:foo" and "data: foo" are both
		// valid. OpenAI always sends the space; a self-hosted OpenAI-compatible
		// base_url (the whole point of multi-provider support) is not
		// guaranteed to, and silently dropping every line otherwise would
		// truncate the stream to nothing.
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Skip malformed chunks rather than aborting the whole stream.
			continue
		}
		if chunk.Error != nil {
			// In-stream error event over a 200 response (OpenAI-compatible
			// proxies report failures this way). Without this check the chunk
			// has zero Choices and would be silently skipped, turning a
			// genuine provider failure into an empty "successful" review.
			return Response{}, fmt.Errorf("llm: %s stream error: %s", c.provider, chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		if err := gate.write(delta); err != nil {
			return Response{}, fmt.Errorf("llm: write stream output: %w", err)
		}
		buf.WriteString(delta)
	}
	// True end of stream: first release any held-back tail that never became
	// the risk-block marker, then emit any incomplete trailing UTF-8 sequence
	// still held by the sanitizer so a legitimately truncated final rune is not
	// lost.
	if err := gate.flush(); err != nil {
		return Response{}, fmt.Errorf("llm: write stream output: %w", err)
	}
	if err := sw.flush(); err != nil {
		return Response{}, fmt.Errorf("llm: write stream output: %w", err)
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("llm: read stream: %w", err)
	}
	if buf.Len() == 0 {
		// Nothing accumulated at all: an empty/malformed 200 body, or a stream
		// with no content deltas. Surfacing an error (instead of a "successful"
		// empty Response) lets the CLI fall back to the buffered path and keeps
		// the empty result out of the shared LLM cache. Note: a stream that
		// produced SOME content but ended without "data: [DONE]" still succeeds
		// — that is the intentional graceful degradation for LimitReader
		// truncation above.
		return Response{}, fmt.Errorf("llm: %s stream ended with no content", c.provider)
	}

	return finishWithRisk(buf.String(), req.Commits, req.Diff), nil
}
