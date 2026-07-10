package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/books"
)

// A discarded line leaves the books. It is how a bad import is undone in an append-only log: the
// imported fact stays, and a later fact supersedes it.
func TestADiscardedLineDoesNotAppearInTheBooks(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))
	importOne(t, log, line("b", 2, -8420, "ACME HARDWARE"))

	if err := books.Discard(log, "human", "imported against the wrong account", "a"); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	txs, err := books.Transactions(log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 1 || txs[0].ID != "b" {
		t.Fatalf("got %v, want only b; the discarded line still shows", txs)
	}
}

// Nothing is deleted. The imported event and the discard both remain, so the history of the mistake
// and its correction survives, and the log's git diff is still a pure append.
func TestDiscardKeepsBothFactsInTheLog(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))
	books.Discard(log, "human", "wrong file", "a")

	events, _ := log.All()
	var imported, discarded bool
	for _, e := range events {
		if e.RecordID == "a" && e.Action == books.ActionImported {
			imported = true
		}
		if e.RecordID == "a" && e.Action == books.ActionDiscarded {
			discarded = true
			if !strings.Contains(string(e.Data), "wrong file") {
				t.Errorf("the reason was not recorded: %s", e.Data)
			}
		}
	}
	if !imported || !discarded {
		t.Fatalf("log should hold both facts: imported=%v discarded=%v", imported, discarded)
	}
}

// The flow the "sources are doors" decision promised. A wrong mapping imports garbage; fixing the
// mapping and re-importing adds the correct line under a new fingerprint but cannot remove the
// garbage, because re-import is a no-op on the fingerprints already there. Discard removes it.
func TestFixingAMappingThenDiscardingLeavesOnlyTheCorrectLine(t *testing.T) {
	log := newLog()
	// The sign was flipped by a bad mapping.
	importOne(t, log, line("wrong", 1, 8420, "ACME HARDWARE"))
	// Re-imported after the fix: different amount, so a different fingerprint.
	importOne(t, log, line("right", 1, -8420, "ACME HARDWARE"))

	if err := books.Discard(log, "human", "sign flipped by the old mapping", "wrong"); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	txs, _ := books.Transactions(log)
	if len(txs) != 1 || txs[0].ID != "right" {
		t.Fatalf("got %v, want only the corrected line", txs)
	}
}

// Re-importing a statement after discarding one of its lines must not bring the line back: the
// imported fact is still present, so the import is a no-op and the discard still stands.
func TestReimportingDoesNotUndoADiscard(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))
	books.Discard(log, "human", "junk", "a")

	// The same line arrives again on an overlapping statement.
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))

	if txs, _ := books.Transactions(log); len(txs) != 0 {
		t.Fatalf("got %v, want none; re-import resurrected a discarded line", txs)
	}
}

func TestDiscardingALineThatWasNeverImportedIsRefused(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))

	if err := books.Discard(log, "human", "", "ghost"); err == nil {
		t.Fatal("discarded a fingerprint that was never imported")
	}
}
