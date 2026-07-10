package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/books"
	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
	"github.com/dallasread/bookkeeper/cli/internal/source"
)

func chequing() source.CSV {
	return source.CSV{
		Account: "Assets:Bank:Chequing", Currency: "CAD",
		Date: "Date", Description: "Description", Amount: "Amount", DateFormat: "2006-01-02",
	}
}

func visa() source.CSV {
	return source.CSV{
		Account: "Liabilities:Card:Visa", Currency: "CAD",
		Date: "Posted", Description: "Merchant", Debit: "Charge", Credit: "Payment", DateFormat: "01/02/2006",
	}
}

func loadSources(t *testing.T, log *eventlog.Log, want ...source.CSV) books.LoadResult {
	t.Helper()
	got, err := books.LoadSources(log, "human", "", want)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	return got
}

func TestLoadingSourcesAddsThemInAccountOrder(t *testing.T) {
	log := newLog()

	got := loadSources(t, log, visa(), chequing())

	if got.Added != 2 {
		t.Errorf("got %+v, want 2 added", got)
	}
	set, _ := books.Sources(log)
	if len(set) != 2 || set[0].Account != "Assets:Bank:Chequing" {
		t.Fatalf("got %v, want them ordered by account", set)
	}
}

func TestLoadingTheSameSourcesTwiceRecordsNothingTheSecondTime(t *testing.T) {
	log := newLog()
	loadSources(t, log, chequing(), visa())

	if got := loadSources(t, log, chequing(), visa()); got != (books.LoadResult{}) {
		t.Errorf("got %+v, want nothing recorded", got)
	}
}

// A mapping that drifts changes how lines normalize, which changes their fingerprints. Recording
// the change is what makes the resulting duplicate diagnosable.
func TestReadingAnAccountWrongAndFixingItIsRecorded(t *testing.T) {
	log := newLog()
	loadSources(t, log, chequing())

	fixed := chequing()
	fixed.Amount = "Amount (CAD)"
	got, err := books.LoadSources(log, "human", "the column was renamed", []source.CSV{fixed})
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if got.Changed != 1 || got.Added != 0 {
		t.Fatalf("got %+v, want 1 changed", got)
	}

	set, _ := books.Sources(log)
	if set[0].Amount != "Amount (CAD)" {
		t.Errorf("amount column = %q", set[0].Amount)
	}

	events, _ := log.All()
	if last := events[len(events)-1]; !strings.Contains(string(last.Data), "the column was renamed") {
		t.Errorf("the reason was not recorded: %s", last.Data)
	}
}

func TestDroppingASourceFromTheFileRemovesIt(t *testing.T) {
	log := newLog()
	loadSources(t, log, chequing(), visa())

	got := loadSources(t, log, chequing())

	if got.Removed != 1 {
		t.Errorf("got %+v, want 1 removed", got)
	}
	if set, _ := books.Sources(log); len(set) != 1 {
		t.Fatalf("got %v, want only chequing", set)
	}
}

func TestASourceCanBeLookedUpByAccount(t *testing.T) {
	log := newLog()
	loadSources(t, log, chequing(), visa())

	got, err := books.Source(log, "Liabilities:Card:Visa")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.Debit != "Charge" {
		t.Errorf("got %+v, want the visa source", got)
	}
}

// Importing against an account nobody taught bookkeeper to read should say what it does know,
// rather than silently normalizing with a zero mapping.
func TestAnUnknownAccountSaysWhichOnesAreKnown(t *testing.T) {
	log := newLog()
	loadSources(t, log, chequing())

	_, err := books.Source(log, "Assets:Bank:Savings")
	if err == nil {
		t.Fatal("found a source that was never loaded")
	}
	if !strings.Contains(err.Error(), "Assets:Bank:Chequing") {
		t.Errorf("the error should name the accounts it knows: %v", err)
	}
}

func TestTwoSourcesForOneAccountAreRefused(t *testing.T) {
	log := newLog()

	if _, err := books.LoadSources(log, "human", "", []source.CSV{chequing(), chequing()}); err == nil {
		t.Fatal("loaded two sources for one account")
	}
	if set, _ := books.Sources(log); len(set) != 0 {
		t.Fatalf("got %v, want nothing written", set)
	}
}

// Currency belongs to the account. Without it the ledger writer has nothing to put on a posting.
func TestASourceWithoutACurrencyIsRefused(t *testing.T) {
	log := newLog()
	broke := chequing()
	broke.Currency = ""

	if _, err := books.LoadSources(log, "human", "", []source.CSV{broke}); err == nil {
		t.Fatal("loaded a source with no currency")
	}
}
