package books_test

import (
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
)

// RecordExport marks that a transaction was written to a connector, and Exported folds those back,
// so the next export skips a deposit already sent and never records rent twice.
func TestRecordExportMarksATransactionExported(t *testing.T) {
	log := newLog()
	if err := books.RecordExport(log, "human", "dep1", "rent", "31", "rr_9", 168000); err != nil {
		t.Fatalf("RecordExport: %v", err)
	}

	exported, err := books.Exported(log)
	if err != nil {
		t.Fatalf("Exported: %v", err)
	}
	if !exported["dep1"] {
		t.Errorf("dep1 not marked exported: %v", exported)
	}
}

// A deposit is recorded to at most one lease, so an export is a once-only fact: recording it again
// is a no-op, not a second rent payment.
func TestATransactionIsExportedAtMostOnce(t *testing.T) {
	log := newLog()
	if err := books.RecordExport(log, "human", "dep1", "rent", "31", "rr_9", 168000); err != nil {
		t.Fatalf("first RecordExport: %v", err)
	}
	if err := books.RecordExport(log, "human", "dep1", "rent", "31", "rr_9", 168000); err != nil {
		t.Fatalf("second RecordExport should be a no-op, got %v", err)
	}

	events, err := log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Action == "exported" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("recorded %d exported events, want exactly 1", n)
	}
}
