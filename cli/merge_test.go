package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two real books on disk, one import: the log is its own interchange format, so bringing a whole
// book in is `bkpr import its/log.jsonl` — no new adapter, no second serialization.
func TestImportAnotherBooksLog(t *testing.T) {
	// The other book: one statement line, a rule that places it.
	bookHere(t)
	csv := filepath.Join(t.TempDir(), "card.csv")
	if err := os.WriteFile(csv, []byte("Date,Description,Amount\n2026-03-07,COFFEE HOUSE 12,-5.00\n"), 0o644); err != nil {
		t.Fatalf("writing csv: %v", err)
	}
	if err := importCmd([]string{csv, "-account", "Liabilities:Card:Visa", "-currency", "CAD", "-amount", "Amount"}); err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if err := ruleSetOne([]string{"coffee", "-category", "Expenses:Meals", "-payee", "Coffee House"}); err != nil {
		t.Fatalf("rules set: %v", err)
	}
	theirLog, err := filepath.Abs(filepath.Join(".bookkeeper", "log.jsonl"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	// This book: empty, then the other book merged in — twice, to prove the no-op.
	bookHere(t)
	if err := importCmd([]string{theirLog}); err != nil {
		t.Fatalf("import log: %v", err)
	}
	if err := importCmd([]string{theirLog}); err != nil {
		t.Fatalf("re-import log: %v", err)
	}

	txs, entries, sum := foldStore(t)
	if sum.Lines != 1 || len(txs) != 1 {
		t.Fatalf("merged book folded to %d lines, want the other book's one", sum.Lines)
	}
	if entries[0].Payee != "Coffee House" || !strings.HasPrefix(entries[0].Postings[0].Account, "Expenses:Meals") {
		t.Errorf("the other book's rule should place its line here too, got %q / %v",
			entries[0].Payee, entries[0].Postings)
	}
}
