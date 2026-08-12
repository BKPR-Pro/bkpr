package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// A browser-script import drives a real browser, which takes many seconds -- longer when a person is clearing a
// 2-step code -- and otherwise looks like a dead hang. spinner shows a single animated status line
// that names the current stage, updated as the script reports progress. It writes only to a terminal;
// piped or redirected (an agent, a log), it stays silent so output is clean.
type spinner struct {
	w   io.Writer
	tty bool

	mu   sync.Mutex
	msg  string
	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// spinnerFrames are braille dots that read as a smooth rotation.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// newSpinner starts a status line showing msg. On a non-terminal writer it does nothing, so set and
// finish are safe no-ops.
func newSpinner(w io.Writer, tty bool, msg string) *spinner {
	s := &spinner{w: w, tty: tty, msg: msg, done: make(chan struct{})}
	if tty {
		s.wg.Add(1)
		go s.loop()
	}
	return s
}

func (s *spinner) loop() {
	defer s.wg.Done()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			msg := s.msg
			s.mu.Unlock()
			// \r returns to the line start, \x1b[2K clears it, so the status updates in place.
			fmt.Fprintf(s.w, "\r\x1b[2K%c %s", spinnerFrames[i%len(spinnerFrames)], msg)
		}
	}
}

// set changes the stage shown on the next tick.
func (s *spinner) set(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()
}

// finish stops the animation and clears the line. It is idempotent, so it can be called the moment
// the browser work is done and again from a defer.
func (s *spinner) finish() {
	s.once.Do(func() {
		if !s.tty {
			return
		}
		close(s.done)
		s.wg.Wait()
		fmt.Fprint(s.w, "\r\x1b[2K")
	})
}
