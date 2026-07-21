package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrStream marks a fatal transport failure of the client stream (oversized
// frame — bufio.ErrTooLong — or an underlying I/O error): bufio.Scanner
// cannot resynchronize after it, so every subsequent Read fails identically
// and Serve shuts down. Distinct from per-line decode errors, which are
// recoverable (line boundaries stay intact).
var ErrStream = errors.New("mcpserver: client stream failed")

// maxLineBytes bounds a single framed message. MCP stdio framing is one JSON
// message per line; the default bufio.Scanner token limit (64 KiB) is too
// small for large tool arguments/results (a review artifact can embed a big
// diff), so we raise it. 32 MiB is generous while still bounding memory
// against a runaway client.
//
// A frame at or above this limit surfaces as bufio.ErrTooLong, which is FATAL
// for the whole Reader: the byte stream is desynchronized mid-frame, so the
// Reader latches the error (see Reader.err) and Server.Serve treats it like a
// dead stream and shuts down — the intended recovery is "restart the server
// process", not mid-stream resynchronization.
const maxLineBytes = 32 << 20 // 32 MiB

// Reader decodes newline-delimited MCP messages from an underlying stream.
//
// Framing (MCP 2025-06-18 stdio transport):
//   - one JSON message per line, delimited by '\n';
//   - messages MUST NOT contain embedded newlines;
//   - UTF-8; JSON-RPC batching was removed in 2025-06-18, so each line is a
//     single JSON object (a leading '[' — a batch array — is rejected).
//
// Reader is NOT safe for concurrent use; Serve owns it from a single
// goroutine.
type Reader struct {
	sc *bufio.Scanner
	// err latches the first fatal (ErrStream) failure. bufio.Scanner alone is
	// not enough: after ErrTooLong it hands out the truncated buffered data as
	// a final token, which would surface as confusing bogus decode errors —
	// so the Reader itself goes sticky on the first fatal error.
	err error
}

// NewReader wraps r with MCP stdio framing.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	return &Reader{sc: sc}
}

// Read returns the next message, or io.EOF when the stream ends. Blank lines
// are skipped (some clients pad output). A line that is not a single JSON
// object yields a parse error but does NOT desynchronize the stream — line
// boundaries are intact, so the caller may keep reading subsequent lines
// (Serve answers such lines with a -32700 Parse error and keeps serving).
//
// A frame exceeding maxLineBytes (bufio.ErrTooLong) is different: it is fatal
// for this Reader — every subsequent Read fails too. See maxLineBytes.
func (r *Reader) Read() (*Message, error) {
	if r.err != nil {
		return nil, r.err
	}
	for {
		if !r.sc.Scan() {
			if err := r.sc.Err(); err != nil {
				r.err = fmt.Errorf("%w: read frame: %s", ErrStream, err)
				return nil, r.err
			}
			return nil, io.EOF
		}
		line := bytes.TrimSpace(r.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		return decodeLine(line)
	}
}

// decodeLine parses one framed line into a Message. A JSON array (batch) is
// rejected explicitly because batching is not part of MCP 2025-06-18.
func decodeLine(line []byte) (*Message, error) {
	if line[0] == '[' {
		return nil, fmt.Errorf("mcpserver: JSON-RPC batching is not supported (spec %s)", ProtocolVersion)
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, fmt.Errorf("mcpserver: decode message: %w", err)
	}
	return &m, nil
}

// Writer encodes MCP messages as newline-delimited JSON to an underlying
// stream. Writes are serialized by a mutex so a future concurrent writer
// (e.g. progress notifications) cannot interleave bytes within a frame.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter wraps w with MCP stdio framing and write serialization.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write encodes m as one line (json + '\n') and writes it atomically w.r.t.
// other Write calls on the same Writer.
//
// json.Marshal escapes control characters (including any '\n' inside string
// values) as \uXXXX / \n escapes, so the encoded body is guaranteed
// single-line; the appended '\n' is purely the frame delimiter.
func (w *Writer) Write(m *Message) error {
	if m.JSONRPC == "" {
		m.JSONRPC = jsonRPCVersion
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("mcpserver: marshal message: %w", err)
	}
	b = append(b, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(b); err != nil {
		return fmt.Errorf("mcpserver: write frame: %w", err)
	}
	return nil
}
