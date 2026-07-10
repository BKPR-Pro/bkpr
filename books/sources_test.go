package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/books"
	"github.com/dallasread/bookkeeper/eventlog"
	"github.com/dallasread/bookkeeper/source"
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

func addSource(t *testing.T, log *eventlog.Log, s source.CSV) {
	t.Helper()
	if err := books.AddSource(log, "human", s); err != nil {
		t.Fatalf("AddSource(%s): %v", s.Account, err)
	}
}

func TestAddingSourcesListsThemInAccountOrder(t *testing.T) {
	log := newLog()
	addSource(t, log, visa())
	addSource(t, log, chequing())

	set, _ := books.Sources(log)
	if len(set) != 2 || set[0].Account != "Assets:Bank:Chequing" {
		t.Fatalf("got %v, want them ordered by account", set)
	}
}

// Add is an upsert keyed by the account. The same source added again is a no-op; a changed one is
// recorded, so a mapping that drifts is diagnosable.
func TestAddingASourceAgainIsANoOpButAChangeIsRecorded(t *testing.T) {
	log := newLog()
	addSource(t, log, chequing())

	addSource(t, log, chequing()) // identical: nothing recorded
	before, _ := log.All()

	fixed := chequing()
	fixed.Amount = "Amount (CAD)"
	addSource(t, log, fixed)
	after, _ := log.All()

	if len(before) != 1 {
		t.Fatalf("an identical re-add recorded something: %d events", len(before))
	}
	if len(after) != 2 {
		t.Fatalf("a real change was not recorded: %d events", len(after))
	}
	if set, _ := books.Sources(log); set[0].Amount != "Amount (CAD)" {
		t.Errorf("amount column = %q", set[0].Amount)
	}
}

func TestRemovingASource(t *testing.T) {
	log := newLog()
	addSource(t, log, chequing())
	addSource(t, log, visa())

	if err := books.RemoveSource(log, "human", "Liabilities:Card:Visa"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if set, _ := books.Sources(log); len(set) != 1 || set[0].Account != "Assets:Bank:Chequing" {
		t.Fatalf("got %v, want only chequing", set)
	}
}

func TestASourceCanBeLookedUpByAccount(t *testing.T) {
	log := newLog()
	addSource(t, log, chequing())
	addSource(t, log, visa())

	got, err := books.Source(log, "Liabilities:Card:Visa")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.Debit != "Charge" {
		t.Errorf("got %+v, want the visa source", got)
	}
}

// Importing against an account nobody taught bookkeeper to read should say what it does know.
func TestAnUnknownAccountSaysWhichOnesAreKnown(t *testing.T) {
	log := newLog()
	addSource(t, log, chequing())

	_, err := books.Source(log, "Assets:Bank:Savings")
	if err == nil {
		t.Fatal("found a source that was never added")
	}
	if !strings.Contains(err.Error(), "Assets:Bank:Chequing") {
		t.Errorf("the error should name the accounts it knows: %v", err)
	}
}

func TestASourceWithoutACurrencyIsRefused(t *testing.T) {
	broke := chequing()
	broke.Currency = ""
	if err := books.AddSource(newLog(), "human", broke); err == nil {
		t.Fatal("added a source with no currency")
	}
}

func TestASourceWithNoAmountColumnsIsRefused(t *testing.T) {
	broke := chequing()
	broke.Amount = ""
	if err := books.AddSource(newLog(), "human", broke); err == nil {
		t.Fatal("added a source with no amount or debit/credit columns")
	}
}
