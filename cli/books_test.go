package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/adapters/ledger"
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

	kept, _, err := filterByAccount([]string{"Uncategorized"}, txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("got %d lines, want both the bare and the leaf Uncategorized", len(kept))
	}

	kept, _, err = filterByAccount([]string{"Materials"}, txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}
	if len(kept) != 1 || kept[0].ID != "known" {
		t.Fatalf("got %v, want only the line posting under Materials", kept)
	}
}

// -account repeats, and a line posting to any named account is kept, so one reading can cover
// several accounts at once.
func TestFilterByAccountTakesSeveralPatterns(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))

	kept, _, err := filterByAccount([]string{"Materials", "^Uncategorized"}, txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("got %d lines, want the union of both patterns", len(kept))
	}
}

// The period is inclusive on both ends, so -from 2026-03-01 -to 2026-03-31 is exactly March, the
// way a person names a month; a zero end leaves that side open.
func TestFilterByDateIsInclusiveOnBothEnds(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t)) // known on 03-01, mystery on 03-02
	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }

	kept, _ := filterByDate(day(1), day(1), txs, entries)
	if len(kept) != 1 || kept[0].ID != "known" {
		t.Fatalf("a one-day period should keep exactly that day's line, got %v", kept)
	}
	kept, _ = filterByDate(day(2), time.Time{}, txs, entries)
	if len(kept) != 1 || kept[0].ID != "mystery" {
		t.Fatalf("an open -to should keep everything from -from on, got %v", kept)
	}
	kept, _ = filterByDate(time.Time{}, day(1), txs, entries)
	if len(kept) != 1 || kept[0].ID != "known" {
		t.Fatalf("an open -from should keep everything up to -to, got %v", kept)
	}
}

func TestPeriodBoundsRefusesAnInvertedPeriod(t *testing.T) {
	if _, _, err := periodBounds("2026-03-31", "2026-03-01"); err == nil {
		t.Error("-to before -from should be refused")
	}
	if _, _, err := periodBounds("march", ""); err == nil {
		t.Error("a date that is not YYYY-MM-DD should be refused")
	}
}

func TestFilterByAccountRefusesABadPattern(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))
	if _, _, err := filterByAccount([]string{"("}, txs, entries); err == nil {
		t.Error("an unparsable pattern should be refused, not treated as matching nothing")
	}
}

// The table is the human surface of the books: the fingerprint every correction is keyed by, in
// the first column, and a footer naming how to see only what is unplaced.
func TestReportListsFingerprints(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))
	sum, err := summarize(txs, entries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	var buf bytes.Buffer
	if err := report(&buf, txs, entries, sum); err != nil {
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

// healthLog holds one of each kind the summary reads: money in, money out, and money whose kind
// is unknown.
func healthLog(t *testing.T) *eventlog.Log {
	t.Helper()
	log := eventlog.New(eventlog.NewMemory())
	if err := books.AddRule(log, "human", rules.Rule{Match: "e-transfer", Category: "Income:Rent", Payee: "J. Smith"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := books.AddRule(log, "human", rules.Rule{Match: "shell", Category: "Expenses:Travel:Fuel"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if _, err := books.Import(log, "statement:march", []model.Transaction{
		{ID: "rent", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: 160000, Scale: 2, Commodity: "CAD"}, Description: "E-TRANSFER FROM J SMITH"},
		{ID: "fuel", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -6240, Scale: 2, Commodity: "CAD"}, Description: "SHELL GAS #123"},
		{ID: "mystery", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -3999, Scale: 2, Commodity: "CAD"}, Description: "UNKNOWN MERCHANT 88"},
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	return log
}

// The health line: income and expenses as positive magnitudes, net as their difference, and the
// unknown-kind money in statement sign, because calling it either would be a guess.
func TestSummarizeComputesTheHealthLine(t *testing.T) {
	txs, entries := foldBooks(t, healthLog(t))
	sum, err := summarize(txs, entries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if sum.Lines != 3 || sum.UncategorizedLines != 1 {
		t.Errorf("lines = %d/%d uncategorized, want 3/1", sum.Lines, sum.UncategorizedLines)
	}
	if len(sum.Totals) != 1 {
		t.Fatalf("totals = %+v, want one commodity", sum.Totals)
	}
	got := sum.Totals[0]
	if got.Income.String() != "1600.00 CAD" || got.Expenses.String() != "62.40 CAD" {
		t.Errorf("income/expenses = %s / %s", got.Income, got.Expenses)
	}
	if got.Net.String() != "1537.60 CAD" {
		t.Errorf("net = %s, want 1537.60 CAD", got.Net)
	}
	if got.Uncategorized.String() != "-39.99 CAD" {
		t.Errorf("uncategorized = %s, want -39.99 CAD (statement sign: money out)", got.Uncategorized)
	}
}

// A hand-kept line can carry its expense on the source-account side (the importer reads the last
// posting as the line's own account); the health line counts it the same as a posting.
func TestSummarizeCountsAnExpenseSourceAccount(t *testing.T) {
	txs := []model.Transaction{{ID: "interest", Account: "Expenses:Mortgage:Interest",
		Date:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: 151174, Scale: 2, Commodity: "CAD"}}}
	entries := []model.Entry{{Postings: []model.Posting{{Account: "Assets:Bank:Chequing",
		Amount: model.Amount{Units: -151174, Scale: 2, Commodity: "CAD"}}}}}

	sum, err := summarize(txs, entries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(sum.Totals) != 1 {
		t.Fatalf("totals = %+v, want one commodity", sum.Totals)
	}
	if got := sum.Totals[0].Expenses.String(); got != "1511.74 CAD" {
		t.Errorf("expenses = %q, want the line's own account counted", got)
	}
	if got := sum.Totals[0].Net.String(); got != "-1511.74 CAD" {
		t.Errorf("net = %q, want -1511.74 CAD", got)
	}
}

// The drift guard: every format renders the one summary computed from the one fold, so the same
// figures must appear in the table, the JSON, and the ledger. A format that computed its own
// numbers would fail here the day it disagreed.
func TestEveryFormatCarriesTheSameSummary(t *testing.T) {
	txs, entries := foldBooks(t, healthLog(t))
	sum, err := summarize(txs, entries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	var table, asJSON, asLedger bytes.Buffer
	if err := report(&table, txs, entries, sum); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := writeJSON(&asJSON, txs, entries, sum); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	if err := ledger.WriteAll(&asLedger, txs, entries); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	if err := writeSummaryComments(&asLedger, sum); err != nil {
		t.Fatalf("writeSummaryComments: %v", err)
	}

	for _, figure := range []string{"1600.00 CAD", "62.40 CAD", "1537.60 CAD", "-39.99 CAD"} {
		for name, out := range map[string]string{"table": table.String(), "json": asJSON.String(), "ledger": asLedger.String()} {
			if !strings.Contains(out, figure) {
				t.Errorf("%s is missing %q:\n%s", name, figure, out)
			}
		}
	}
}

// The JSON is the same reading for a machine: valid, fingerprint-bearing, with where each line
// currently posts, so an external model can parse the queue and answer back through categorize
// and rules set.
func TestWriteJSONRoundTrips(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))
	kept, keptEntries, err := filterByAccount([]string{"^Uncategorized"}, txs, entries)
	if err != nil {
		t.Fatalf("filterByAccount: %v", err)
	}
	sum, err := summarize(kept, keptEntries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	var buf bytes.Buffer
	if err := writeJSON(&buf, kept, keptEntries, sum); err != nil {
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
