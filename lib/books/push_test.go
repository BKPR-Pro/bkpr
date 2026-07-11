package books_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
)

// RecordPush marks that a transaction was recorded to a destination, and Pushed folds those back,
// so the next push skips a deposit already sent and never records rent twice.
func TestRecordPushMarksATransactionPushed(t *testing.T) {
	log := newLog()
	if err := books.RecordPush(log, "human", "dep1", "rent", "31", "rr_9", 168000); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}

	pushed, err := books.Pushed(log)
	if err != nil {
		t.Fatalf("Pushed: %v", err)
	}
	if !pushed["dep1"] {
		t.Errorf("dep1 not marked pushed: %v", pushed)
	}
}

// A deposit is recorded to at most one lease, so a push is a once-only fact: recording it again is a
// no-op, not a second rent payment.
func TestATransactionIsPushedAtMostOnce(t *testing.T) {
	log := newLog()
	if err := books.RecordPush(log, "human", "dep1", "rent", "31", "rr_9", 168000); err != nil {
		t.Fatalf("first RecordPush: %v", err)
	}
	if err := books.RecordPush(log, "human", "dep1", "rent", "31", "rr_9", 168000); err != nil {
		t.Fatalf("second RecordPush should be a no-op, got %v", err)
	}

	events, err := log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Action == "pushed" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("recorded %d pushed events, want exactly 1", n)
	}
}
