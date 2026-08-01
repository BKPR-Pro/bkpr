package books_test

import (
	"strings"
	"testing"

	"bkpr.pro/bkpr/lib/books"
)

// A voided line leaves the books. It is how a bad import is undone in an append-only log: the
// imported fact stays, and a later fact supersedes it.
func TestAVoidedLineDoesNotAppearInTheBooks(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))
	importOne(t, log, line("b", 2, -8420, "ACME HARDWARE"))

	if err := books.VoidTransaction(log, "human", "imported against the wrong account", "a"); err != nil {
		t.Fatalf("VoidTransaction: %v", err)
	}

	txs, err := books.Transactions(log)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 1 || txs[0].ID != "b" {
		t.Fatalf("got %v, want only b; the voided line still shows", txs)
	}
}

// Nothing is deleted. The imported event and the void both remain, so the history of the mistake
// and its correction survives, and the log's git diff is still a pure append.
func TestVoidKeepsBothFactsInTheLog(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))
	books.VoidTransaction(log, "human", "wrong file", "a")

	events, _ := log.All()
	var imported, voided bool
	for _, e := range events {
		if e.RecordID == "a" && e.Action == books.ActionImported {
			imported = true
		}
		if e.RecordID == "a" && e.Action == books.ActionVoided {
			voided = true
			if !strings.Contains(string(e.Data), "wrong file") {
				t.Errorf("the reason was not recorded: %s", e.Data)
			}
		}
	}
	if !imported || !voided {
		t.Fatalf("log should hold both facts: imported=%v voided=%v", imported, voided)
	}
}

// The flow the "sources are doors" decision promised. A wrong mapping imports garbage; fixing the
// mapping and re-importing adds the correct line under a new fingerprint but cannot remove the
// garbage, because re-import is a no-op on the fingerprints already there. Voiding removes it.
func TestFixingAMappingThenVoidingLeavesOnlyTheCorrectLine(t *testing.T) {
	log := newLog()
	// The sign was flipped by a bad mapping.
	importOne(t, log, line("wrong", 1, 8420, "ACME HARDWARE"))
	// Re-imported after the fix: different amount, so a different fingerprint.
	importOne(t, log, line("right", 1, -8420, "ACME HARDWARE"))

	if err := books.VoidTransaction(log, "human", "sign flipped by the old mapping", "wrong"); err != nil {
		t.Fatalf("VoidTransaction: %v", err)
	}

	txs, _ := books.Transactions(log)
	if len(txs) != 1 || txs[0].ID != "right" {
		t.Fatalf("got %v, want only the corrected line", txs)
	}
}

// Re-importing a statement after voiding one of its lines must not bring the line back: the imported
// fact is still present, so the import is a no-op and the void still stands.
func TestReimportingDoesNotUndoAVoid(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))
	books.VoidTransaction(log, "human", "junk", "a")

	// The same line arrives again on an overlapping statement.
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))

	if txs, _ := books.Transactions(log); len(txs) != 0 {
		t.Fatalf("got %v, want none; re-import resurrected a voided line", txs)
	}
}

func TestVoidingALineThatWasNeverImportedIsRefused(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -6240, "SHELL GAS"))

	if err := books.VoidTransaction(log, "human", "", "ghost"); err == nil {
		t.Fatal("voided a fingerprint that was never imported")
	}
}
