package cli

// waitIndicator is a minimal, stdlib-only "waiting for the model" spinner for
// the buffered (non-streaming) LLM call sites (U2). The streaming path gives
// live token feedback and never needs it; the buffered path is otherwise
// completely silent for seconds to tens of seconds.
//
// Invariants (see docs/plans/feat-waiting-for-model-indicator.md):
//   - writes ONLY to the provided writer (the command's stderr) — stdout
//     carries the review body / --format=json and must stay byte-identical;
//   - TTY-gated: on a non-TTY stderr (CI logs, pipes) it stays fully silent,
//     so no `\r` spam ever reaches a log file;
//   - stop() is idempotent (sync.Once) and clears the spinner line before any
//     subsequent output, so no frame char leaks into the rendered review.

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// waitingForModelMsg is the shared indicator text for every buffered LLM wait.
const waitingForModelMsg = "waiting for model response..."

type waitIndicator struct {
	w       io.Writer // the command's stderr
	isTTY   bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	once    sync.Once
	started bool
}

// newWaitIndicator builds an indicator writing to w (pass cmd.ErrOrStderr()).
// Whether it animates is decided once here, via the same TTY detection the
// streaming path uses (term.go isTerminal).
func newWaitIndicator(w io.Writer) *waitIndicator {
	return &waitIndicator{
		w:      w,
		isTTY:  isTerminal(w),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// start begins animating msg on a TTY; on a non-TTY it does nothing (silent —
// no CI log noise). The first frame is drawn synchronously inside the spinner
// goroutine before it starts ticking, so even a near-instant call renders and
// then cleanly erases exactly one frame.
func (wi *waitIndicator) start(msg string) {
	if !wi.isTTY {
		return
	}
	wi.started = true
	go func() {
		defer close(wi.doneCh)
		// ASCII-only frames: portable, matches the project's conservative
		// terminal handling (no unicode braille).
		frames := []rune{'|', '/', '-', '\\'}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		fmt.Fprintf(wi.w, "\r%c %s", frames[i], msg)
		for {
			select {
			case <-wi.stopCh:
				return
			case <-ticker.C:
				i = (i + 1) % len(frames)
				fmt.Fprintf(wi.w, "\r%c %s", frames[i], msg)
			}
		}
	}()
}

// stop halts the spinner and erases its line (`\r` + ANSI erase-to-EOL) so
// subsequent output starts clean. Idempotent: a second stop (e.g. a deferred
// safety stop after an explicit one) is a no-op. Safe to call even when start
// never animated (non-TTY / never started).
func (wi *waitIndicator) stop() {
	wi.once.Do(func() {
		if !wi.started {
			return
		}
		close(wi.stopCh)
		<-wi.doneCh
		fmt.Fprint(wi.w, "\r\033[K")
	})
}
