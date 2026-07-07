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

	var buf strings.Builder
	scanner := bufio.NewScanner(resp.Body)
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
		if _, err := io.WriteString(w, delta); err != nil {
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
