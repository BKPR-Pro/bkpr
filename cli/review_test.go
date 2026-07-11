package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
)

func reviewLog(t *testing.T) *eventlog.Log {
	t.Helper()
	log := eventlog.New(eventlog.NewMemory())
	if err := books.AddRule(log, "human", rules.Rule{Match: "acme", Category: "Expenses:Materials"}, ""); err != nil {
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

// review is a generic report of the whole books, not a queue: every entry is present, placed or not,
// each with the fingerprint an authoring command writes back against.
func TestReviewReportsEveryEntry(t *testing.T) {
	rep, err := reviewData(reviewLog(t))
	if err != nil {
		t.Fatalf("reviewData: %v", err)
	}

	if len(rep.Entries) != 2 {
		t.Fatalf("got %d entries, want both lines (placed and not)", len(rep.Entries))
	}
	byID := map[string]reviewEntry{}
	for _, e := range rep.Entries {
		byID[e.Fingerprint] = e
	}
	if got := byID["known"]; len(got.Postings) != 1 || got.Postings[0].Account != "Expenses:Materials" {
		t.Errorf("placed line = %+v, want its rule's account", got)
	}
	mystery := byID["mystery"]
	if mystery.Amount != "-15.00 CAD" || mystery.Description != "WHO KNOWS" {
		t.Errorf("mystery = %+v, want the line's own fields", mystery)
	}
	if len(mystery.Postings) != 1 || mystery.Postings[0].Account != "Uncategorized" {
		t.Errorf("mystery posts to %v, want [Uncategorized] so the reader can find it", mystery.Postings)
	}
}

// A price on a posting (a share bought with cash) is carried, so the report is the whole entry, not a
// single-commodity summary.
func TestReviewCarriesPostingPrices(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	buy := model.Transaction{ID: "buy", Account: "Assets:Brokerage:Cash",
		Date: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Amount: model.Amount{Units: -100000, Scale: 2, Commodity: "USD"}}
	books.Import(log, "statement:brokerage", []model.Transaction{buy})
	cost := model.Amount{Units: 100000, Scale: 2, Commodity: "USD"}
	books.Categorize(log, "human", "", "buy", "Bought Apple",
		[]model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: &cost}})

	rep, err := reviewData(log)
	if err != nil {
		t.Fatalf("reviewData: %v", err)
	}
	if len(rep.Entries) != 1 || len(rep.Entries[0].Postings) != 1 {
		t.Fatalf("got %+v", rep.Entries)
	}
	p := rep.Entries[0].Postings[0]
	if p.Amount != "10 AAPL" || p.Cost != "1000.00 USD" {
		t.Errorf("posting = %+v, want 10 AAPL @@ 1000.00 USD", p)
	}
}

// The output is valid JSON, so whoever operates the tool can parse it.
func TestReviewReportMarshalsToJSON(t *testing.T) {
	rep, _ := reviewData(reviewLog(t))
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back reviewReport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(back.Entries) != 2 {
		t.Errorf("round-tripped %+v", back)
	}
}
