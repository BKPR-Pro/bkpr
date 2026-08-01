package main

import (
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

func twinTx(id, account string, day int, cents int64, description string) model.Transaction {
	return model.Transaction{
		ID: id, Account: account, Description: description,
		Date:   time.Date(2026, 7, day, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: cents, Scale: 2, Commodity: "CAD"},
	}
}

// An import is the moment a duplicate is created, and the moment nothing looks for one. The sweep that
// can see them exists -- register -dups -- but only a person who thinks to run it ever does, and the
// import that just landed 110 duplicates reported a clean run. Check the lines the import actually
// landed, against everything already in the book, and say so.
func TestImportWarnsWhenALandedLineTwinsAnExistingOne(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	// The line as the bank first served it.
	if _, err := books.Import(log, "connector:rbc", []model.Transaction{
		twinTx("aaa", "Assets:Bank:Chequing", 14, -15500, "Online Banking payment"),
	}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// The same charge, re-served later under the fuller memo the bank now prints.
	result, err := books.Import(log, "connector:rbc", []model.Transaction{
		twinTx("bbb", "Assets:Bank:Chequing", 14, -15500, "Online Banking payment - 7604 PROV NB PROP TX"),
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(result.IDs) != 1 || result.IDs[0] != "bbb" {
		t.Fatalf("import should report the line it landed, got %v", result.IDs)
	}

	groups, err := twinsAmong(log, result.IDs)
	if err != nil {
		t.Fatalf("twinsAmong: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want the memo-drifted double this import just created", len(groups))
	}
}

// A clean import must stay quiet, or the warning is noise on every run.
func TestImportIsQuietWhenNothingTwins(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	books.Import(log, "connector:rbc", []model.Transaction{
		twinTx("aaa", "Assets:Bank:Chequing", 14, -15500, "Online Banking payment"),
	})
	result, err := books.Import(log, "connector:rbc", []model.Transaction{
		twinTx("ccc", "Assets:Bank:Chequing", 20, -4998, "THE BORDER CAFE"),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	groups, err := twinsAmong(log, result.IDs)
	if err != nil {
		t.Fatalf("twinsAmong: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("got %d groups, want none: nothing about this line collides", len(groups))
	}
}
