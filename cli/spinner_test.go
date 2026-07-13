package main

import (
	"bytes"
	"strings"
	"testing"
)

// Piped or redirected (an agent, a log), the spinner stays silent, and set/finish are safe no-ops
// callable more than once.
func TestSpinnerSilentWithoutTerminal(t *testing.T) {
	var buf bytes.Buffer
	s := newSpinner(&buf, false, "connecting")
	s.set("signing in")
	s.finish()
	s.finish() // idempotent

	if buf.Len() != 0 {
		t.Errorf("a non-terminal spinner wrote %q, want nothing", buf.String())
	}
}

// On a terminal it animates and, once finished, leaves the line cleared rather than a stray frame.
func TestSpinnerClearsTheLineOnFinish(t *testing.T) {
	var buf bytes.Buffer
	s := newSpinner(&buf, true, "connecting")
	s.finish()

	// The last thing written must be a clear so no status text is left on screen.
	if !strings.HasSuffix(buf.String(), "\r\x1b[2K") {
		t.Errorf("finish did not clear the line; output ended with %q", buf.String())
	}
}
