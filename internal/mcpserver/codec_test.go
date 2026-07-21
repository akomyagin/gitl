package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReaderClassifiesMessages(t *testing.T) {
	tests := []struct {
		name         string
		line         string
		wantRequest  bool
		wantResponse bool
		wantNotif    bool
		wantMethod   string
	}{
		{
			name:        "request with int id",
			line:        `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			wantRequest: true,
			wantMethod:  "tools/list",
		},
		{
			name:        "request with string id",
			line:        `{"jsonrpc":"2.0","id":"abc","method":"initialize","params":{}}`,
			wantRequest: true,
			wantMethod:  "initialize",
		},
		{
			name:         "success response",
			line:         `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
			wantResponse: true,
		},
		{
			name:         "error response",
			line:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`,
			wantResponse: true,
		},
		{
			name:       "notification (no id)",
			line:       `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			wantNotif:  true,
			wantMethod: "notifications/initialized",
		},
		{
			name:       "notification with explicit null id",
			line:       `{"jsonrpc":"2.0","id":null,"method":"notifications/initialized"}`,
			wantNotif:  true,
			wantMethod: "notifications/initialized",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewReader(strings.NewReader(tc.line + "\n")).Read()
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got := m.IsRequest(); got != tc.wantRequest {
				t.Errorf("IsRequest=%v want %v", got, tc.wantRequest)
			}
			if got := m.IsResponse(); got != tc.wantResponse {
				t.Errorf("IsResponse=%v want %v", got, tc.wantResponse)
			}
			if got := m.IsNotification(); got != tc.wantNotif {
				t.Errorf("IsNotification=%v want %v", got, tc.wantNotif)
			}
			if tc.wantMethod != "" && m.Method != tc.wantMethod {
				t.Errorf("Method=%q want %q", m.Method, tc.wantMethod)
			}
		})
	}
}

func TestReaderResyncsAfterMalformedLine(t *testing.T) {
	// A bad line yields an error but does not desynchronize the stream: the
	// next Read returns the following (valid) frame.
	in := `{"jsonrpc":"2.0",oops` + "\n" +
		`[{"jsonrpc":"2.0","id":1,"method":"a"}]` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	r := NewReader(strings.NewReader(in))

	if _, err := r.Read(); err == nil {
		t.Fatal("expected error for invalid json line")
	} else if errors.Is(err, ErrStream) {
		t.Fatalf("decode error must not be ErrStream: %v", err)
	}
	if _, err := r.Read(); err == nil {
		t.Fatal("expected error for batch array line")
	}
	m, err := r.Read()
	if err != nil {
		t.Fatalf("Read after bad lines: %v", err)
	}
	if m.Method != "tools/list" || string(m.ID) != "2" {
		t.Fatalf("resynced frame mismatch: method=%q id=%s", m.Method, m.ID)
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReaderSkipsBlankLines(t *testing.T) {
	in := "\n  \n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n"
	r := NewReader(strings.NewReader(in))
	m, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.Method != "ping" {
		t.Fatalf("Method=%q want ping", m.Method)
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReaderOversizedFrameIsFatal(t *testing.T) {
	// A frame beyond maxLineBytes surfaces as ErrStream (bufio.ErrTooLong
	// underneath) and the Reader stays errored — no resync.
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":1,"method":"x","params":{"blob":"`)
	b.Write(bytes.Repeat([]byte("a"), maxLineBytes+1))
	b.WriteString(`"}}` + "\n")
	b.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")

	r := NewReader(&b)
	if _, err := r.Read(); !errors.Is(err, ErrStream) {
		t.Fatalf("expected ErrStream for oversized frame, got %v", err)
	}
	if _, err := r.Read(); !errors.Is(err, ErrStream) {
		t.Fatalf("expected Reader to stay errored, got %v", err)
	}
}

// failingReader always fails with a non-EOF I/O error.
type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestReaderIOErrorIsFatal(t *testing.T) {
	r := NewReader(failingReader{err: errors.New("boom")})
	if _, err := r.Read(); !errors.Is(err, ErrStream) {
		t.Fatalf("expected ErrStream for I/O error, got %v", err)
	}
}

func TestWriterEncodesSingleLine(t *testing.T) {
	// A result whose text contains embedded newlines must still encode to a
	// single physical line (json escapes them), preserving stdio framing.
	var buf bytes.Buffer
	w := NewWriter(&buf)
	m := newResult(json.RawMessage("7"),
		json.RawMessage(`{"content":[{"type":"text","text":"line1\nline2"}]}`))
	if err := w.Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b := buf.Bytes()
	if bytes.Count(b, []byte("\n")) != 1 || b[len(b)-1] != '\n' {
		t.Fatalf("encoded frame is not exactly one line: %q", b)
	}
}

func TestWriterReaderRoundTrip(t *testing.T) {
	msgs := []*Message{
		{JSONRPC: jsonRPCVersion, ID: json.RawMessage("1"), Method: "initialize",
			Params: json.RawMessage(`{"protocolVersion":"2025-06-18"}`)},
		{JSONRPC: jsonRPCVersion, Method: "notifications/initialized"},
		newResult(json.RawMessage(`"str-id"`), json.RawMessage(`{"ok":true}`)),
		newError(json.RawMessage("2"), CodeMethodNotFound, "no such method"),
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, m := range msgs {
		if err := w.Write(m); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	r := NewReader(&buf)
	for i, want := range msgs {
		got, err := r.Read()
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if got.Method != want.Method {
			t.Errorf("#%d Method=%q want %q", i, got.Method, want.Method)
		}
		if string(got.ID) != string(want.ID) {
			t.Errorf("#%d ID=%q want %q", i, got.ID, want.ID)
		}
		if want.Error != nil && (got.Error == nil || got.Error.Code != want.Error.Code) {
			t.Errorf("#%d error mismatch: %+v", i, got.Error)
		}
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF at end, got %v", err)
	}
}
