package main

import (
	"strings"
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/model"
	"github.com/BKPR-Pro/bkpr/lib/store"
)

// FR-17's detector only ever runs against the lines an import just landed, so a duplicate created
// weeks earlier -- one side categorized on an old door, its twin still sitting in Uncategorized --
// goes undetected until someone hand-categorizes it, with no warning at the moment that happens.
// categorize should run the same date+amount-regardless-of-door check on the line it just asserted.
func TestCategorizeWarnsWhenTheLineTwinsAnAlreadyCategorizedOne(t *testing.T) {
	bookHere(t)

	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := books.Import(s.Log, "connector:pc-mastercard", []model.Transaction{
		{ID: "old", Account: "Liabilities:PC Mastercard", Description: "COSTCO WHOLESALE",
			Date:   time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -8842, Scale: 2, Commodity: "CAD"}},
	}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// The migration's purpose-split door re-serves the same charge under a different description,
	// weeks before anyone categorizes either line.
	if _, err := books.Import(s.Log, "connector:pc-mastercard:9-birch", []model.Transaction{
		{ID: "new", Account: "Liabilities:PC Mastercard:9 Birch Street", Description: "Costco Appliances",
			Date:   time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
			Amount: model.Amount{Units: -8842, Scale: 2, Commodity: "CAD"}},
	}); err != nil {
		t.Fatalf("second import: %v", err)
	}
	s.Close()

	// The old line gets categorized first, same as it would during a normal categorization pass.
	if err := categorize([]string{"old", "-category", "Expenses:Materials"}); err != nil {
		t.Fatalf("categorize old: %v", err)
	}

	out, err := withPipedStdout(t, func() error {
		return categorize([]string{"new", "-category", "Expenses:Materials"})
	})
	if err != nil {
		t.Fatalf("categorize new: %v", err)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("categorize output = %q, want a warning: this line twins an already-categorized one", out)
	}
}

// A line with no twin anywhere in the book must categorize quietly, or the warning is noise on every
// ordinary categorization.
func TestCategorizeIsQuietWhenNothingTwins(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	out, err := withPipedStdout(t, func() error {
		return categorize([]string{"tx1", "-category", "Expenses:Food"})
	})
	if err != nil {
		t.Fatalf("categorize: %v", err)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("categorize output = %q, want no warning: nothing about this line collides", out)
	}
}
