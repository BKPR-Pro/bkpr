package main

import (
	"io"
	"os"
	"testing"
)

// notATerminal hands back one end of a pipe, which isTerminal always refuses (the same ioctl a real
// tty answers fails on a pipe), so a test can exercise the "not a person watching" branch without a
// real terminal.
func notATerminal(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return w
}

func TestDefaultFormatPicksMachineWhenNotATerminal(t *testing.T) {
	if got := defaultFormat(notATerminal(t), "table", "json"); got != "json" {
		t.Errorf("defaultFormat = %q, want json off a pipe", got)
	}
}

// withPipedStdout swaps os.Stdout for a pipe's write end for the duration of fn, so a command that
// writes straight to os.Stdout (as books, report, and receipt do when -out is empty) can be run
// against something isTerminal reports false for, and its output read back and checked. It restores
// os.Stdout before returning, so a later test's own terminal (or lack of one) is undisturbed.
func withPipedStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(out), runErr
}
