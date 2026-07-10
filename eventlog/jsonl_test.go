package eventlog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/eventlog"
)

// The file is the book of record, so a fact survives the process that wrote it.
func TestJSONLReopensWithEveryEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")

	first, err := eventlog.OpenJSONL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log := eventlog.New(first)
	for _, action := range []string{"imported", "categorized"} {
		if _, err := log.Track(fact("abc", action)); err != nil {
			t.Fatalf("track: %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := eventlog.OpenJSONL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	events, _ := second.All()
	if len(events) != 2 {
		t.Fatalf("reopened with %d events, want 2", len(events))
	}
	// once-only state has to come back too, or a re-import after a restart double-books.
	if _, err := eventlog.New(second).TrackOnce(fact("abc", "imported")); err == nil {
		t.Error("a restart forgot that the line was already imported")
	}
}

// The file is committed to git and greppable, so it really is one JSON object per line.
func TestJSONLWritesOneObjectPerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	s, _ := eventlog.OpenJSONL(path)
	log := eventlog.New(s)
	log.Track(fact("a", "imported"))
	log.Track(fact("b", "imported"))
	s.Close()

	body, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], `{"id":`) {
		t.Errorf("a line is not a JSON object: %s", lines[0])
	}
}

// A crash mid-append can leave a torn final line. Since the log only ever grows, only the last
// line can be partial, so it is dropped and the file healed rather than the whole log refused.
func TestJSONLHealsATornFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	s, _ := eventlog.OpenJSONL(path)
	log := eventlog.New(s)
	whole, _ := log.Track(fact("a", "imported"))
	s.Close()

	// Simulate a crash partway through the next append.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"id":"b","collection":"transa`)
	f.Close()

	reopened, err := eventlog.OpenJSONL(path)
	if err != nil {
		t.Fatalf("a torn final line should be healed, not fatal: %v", err)
	}
	defer reopened.Close()

	events, _ := reopened.All()
	if len(events) != 1 || events[0].ID != whole.ID {
		t.Fatalf("got %d events, want only the whole one", len(events))
	}

	// The heal is durable: a fresh append lands cleanly and the file has exactly two lines.
	if _, err := eventlog.New(reopened).Track(fact("c", "imported")); err != nil {
		t.Fatalf("append after heal: %v", err)
	}
	body, _ := os.ReadFile(path)
	if lines := strings.Count(strings.TrimRight(string(body), "\n"), "\n") + 1; lines != 2 {
		t.Fatalf("file has %d lines after heal + append, want 2:\n%s", lines, body)
	}
}

// A line that will not parse anywhere but the end means the history was corrupted or hand-edited.
// The books are money, so that is refused rather than guessed at.
func TestJSONLRefusesCorruptionInTheMiddle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"+`{"id":"a","collection":"c","record_id":"r","action":"imported","version":1,"actor":"x","data":null}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := eventlog.OpenJSONL(path); err == nil {
		t.Fatal("opened a log with a corrupt line in the middle")
	}
}

// One writer at a time, which is what lets AppendOnce trust its in-memory set.
func TestJSONLLocksAgainstASecondWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	first, err := eventlog.OpenJSONL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.Close()

	if _, err := eventlog.OpenJSONL(path); err == nil {
		t.Fatal("a second writer opened a log already held")
	}
}
