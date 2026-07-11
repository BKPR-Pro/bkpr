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

// review is the queue a model drives from: only the lines the rules could not place, each with the
// fingerprint the model writes back against and enough of the line to decide.
func TestReviewListsOnlyTheUncategorizedLines(t *testing.T) {
	rep, err := reviewData(reviewLog(t))
	if err != nil {
		t.Fatalf("reviewData: %v", err)
	}

	if len(rep.Uncategorized) != 1 {
		t.Fatalf("got %d uncategorized, want 1 (the matched line is placed)", len(rep.Uncategorized))
	}
	got := rep.Uncategorized[0]
	if got.Fingerprint != "mystery" {
		t.Errorf("fingerprint = %q, want the unmatched line", got.Fingerprint)
	}
	if got.Date != "2026-03-02" || got.Account != "Assets:Bank:Chequing" || got.Amount != "-15.00 CAD" {
		t.Errorf("item = %+v, want the line's own fields", got)
	}
	if got.Description != "WHO KNOWS" {
		t.Errorf("description = %q", got.Description)
	}
	if len(got.PostsTo) != 1 || got.PostsTo[0] != "Uncategorized" {
		t.Errorf("posts_to = %v, want [Uncategorized]", got.PostsTo)
	}
}

// A leaf-uncategorized line (the kind is known, the detail is not) is still in the queue, so a model
// can resolve the property a hardware charge served.
func TestReviewIncludesAnUncategorizedLeaf(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	books.AddRule(log, "human", rules.Rule{Match: "acme", Category: "Expenses:Materials:Uncategorized"}, "")
	books.Import(log, "statement:march", []model.Transaction{
		{ID: "leaf", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -8420, Scale: 2, Commodity: "CAD"}, Description: "ACME HARDWARE"},
	})

	rep, err := reviewData(log)
	if err != nil {
		t.Fatalf("reviewData: %v", err)
	}
	if len(rep.Uncategorized) != 1 || rep.Uncategorized[0].PostsTo[0] != "Expenses:Materials:Uncategorized" {
		t.Fatalf("got %+v, want the leaf line with its partial account", rep.Uncategorized)
	}
}

// The table is the human surface of the queue: the fingerprint every correction is keyed by, in
// the first column, and a closing line naming the two ways to answer.
func TestReviewTableListsTheFingerprints(t *testing.T) {
	rep, err := reviewData(reviewLog(t))
	if err != nil {
		t.Fatalf("reviewData: %v", err)
	}

	var buf bytes.Buffer
	if err := reviewTable(&buf, rep); err != nil {
		t.Fatalf("reviewTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "FINGERPRINT") || !strings.Contains(out, "mystery") {
		t.Errorf("table should carry the fingerprint column:\n%s", out)
	}
	if !strings.Contains(out, "categorize <fingerprint>") {
		t.Errorf("table should close by naming the next step:\n%s", out)
	}
}

// An empty queue says so, rather than printing a bare header over nothing.
func TestReviewTableSaysWhenThereIsNothingToPlace(t *testing.T) {
	var buf bytes.Buffer
	if err := reviewTable(&buf, reviewReport{}); err != nil {
		t.Fatalf("reviewTable: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to review") {
		t.Errorf("got %q, want the all-clear", buf.String())
	}
}

// The output is valid JSON keyed by fingerprint-bearing items, so an external model can parse it.
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
	if len(back.Uncategorized) != 1 || back.Uncategorized[0].Fingerprint != "mystery" {
		t.Errorf("round-tripped %+v", back)
	}
}
