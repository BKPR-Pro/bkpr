package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
)

// booksLog holds the two kinds of line every reading contains: one a rule places (to a truncated
// leaf, so the kind is known but not the detail) and one no rule knows at all.
func booksLog(t *testing.T) *eventlog.Log {
	t.Helper()
	log := eventlog.New(eventlog.NewMemory())
	if err := books.AddRule(log, "human", rules.Rule{Match: "acme", Category: "Expenses:Materials:Uncategorized"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if _, err := books.Import(log, "statement:march", []model.Transaction{
		{ID: "known", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -8420, Scale: 2, Commodity: "CAD"}, Description: "ACME HARDWARE #4471"},
		{ID: "mystery", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -1500, Scale: 2, Commodity: "CAD"}, Description: "WHO KNOWS"},
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	return log
}

func foldBooks(t *testing.T, log *eventlog.Log) ([]model.Transaction, []model.Entry) {
	t.Helper()
	txs, entries, err := books.Ledger(log)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	return txs, entries
}

// One pattern finds Uncategorized at every depth - bare, and as a truncated leaf - because the
// match runs over the whole account path, the way ledger matches account names.
func TestFilterByAccountFindsUncategorizedAtAnyDepth(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))

	kept, _, err := filterByAccount("Uncategorized", txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("got %d lines, want both the bare and the leaf Uncategorized", len(kept))
	}

	kept, _, err = filterByAccount("Materials", txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}
	if len(kept) != 1 || kept[0].ID != "known" {
		t.Fatalf("got %v, want only the line posting under Materials", kept)
	}
}

func TestFilterByAccountRefusesABadPattern(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))
	if _, _, err := filterByAccount("(", txs, entries); err == nil {
		t.Error("an unparsable pattern should be refused, not treated as matching nothing")
	}
}

// The table is the human surface of the books: the fingerprint every correction is keyed by, in
// the first column, and a footer naming how to see only what is unplaced.
func TestReportListsFingerprints(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))

	var buf bytes.Buffer
	if err := report(&buf, txs, entries); err != nil {
		t.Fatalf("report: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "FINGERPRINT") || !strings.Contains(out, "mystery") {
		t.Errorf("table should carry the fingerprint column:\n%s", out)
	}
	if !strings.Contains(out, "bk books -account Uncategorized") {
		t.Errorf("table should name how to see only the unplaced lines:\n%s", out)
	}
}

// The JSON is the same reading for a machine: valid, fingerprint-bearing, with where each line
// currently posts, so an external model can parse the queue and answer back through categorize
// and rules set.
func TestWriteJSONRoundTrips(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))
	kept, keptEntries, err := filterByAccount("^Uncategorized", txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}

	var buf bytes.Buffer
	if err := writeJSON(&buf, kept, keptEntries); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	var back struct {
		Lines []bookLine `json:"lines"`
	}
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(back.Lines) != 1 || back.Lines[0].Fingerprint != "mystery" {
		t.Fatalf("round-tripped %+v, want the one bare-Uncategorized line", back.Lines)
	}
	got := back.Lines[0]
	if got.Date != "2026-03-02" || got.Amount != "-15.00 CAD" || got.Description != "WHO KNOWS" {
		t.Errorf("line = %+v, want the line's own fields", got)
	}
	if len(got.PostsTo) != 1 || got.PostsTo[0] != "Uncategorized" {
		t.Errorf("posts_to = %v, want [Uncategorized]", got.PostsTo)
	}
}
