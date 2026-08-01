package main

import (
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

func memLog() *eventlog.Log { return eventlog.New(eventlog.NewMemory()) }

func cad(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"} }

// The literal "next" is the only value resolved; everything else is the number the caller gave,
// verbatim, so a book that numbers its own way is untouched.
func TestInvoiceNumberResolvesOnlyNext(t *testing.T) {
	log := memLog()
	got, err := invoiceNumber(log, "INV-7c")
	if err != nil {
		t.Fatalf("invoiceNumber: %v", err)
	}
	if got != "INV-7c" {
		t.Errorf("number = %q, want the caller's own", got)
	}
	if got, err = invoiceNumber(log, ""); err != nil || got != "" {
		t.Errorf("number = %q, %v; an unnumbered invoice stays unnumbered", got, err)
	}
	if got, err = invoiceNumber(log, "next"); err != nil || got != "1" {
		t.Errorf("number = %q, %v; want the first number in an empty book", got, err)
	}
}

// Raising the same invoice twice with -invoice next must not burn a number or renumber the one
// already recorded: the number is not part of the fingerprint, so the second raise is a no-op and
// the sequence has not moved.
func TestRaisingTwiceWithNextBurnsNoNumber(t *testing.T) {
	log := memLog()
	inv := books.Invoice{
		Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Party: "Commercial Tenant",
		Amount: cad(160000), Category: "Income:Rent",
	}

	for run := 0; run < 2; run++ {
		number, err := invoiceNumber(log, "next")
		if err != nil {
			t.Fatalf("invoiceNumber: %v", err)
		}
		inv.Number = number
		if _, added, err := books.Raise(log, "human", "", inv); err != nil {
			t.Fatalf("Raise: %v", err)
		} else if added != (run == 0) {
			t.Fatalf("run %d added = %v, want the second raise to be a no-op", run, added)
		}
	}

	invs, err := books.Invoices(log)
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	if len(invs) != 1 || invs[0].Number != "1" {
		t.Fatalf("invoices = %+v, want the one invoice still numbered 1", invs)
	}
	if got, err := books.NextInvoiceNumber(log); err != nil || got != "2" {
		t.Errorf("next = %q, %v; want 2 -- the second run spent nothing", got, err)
	}
}
