package books_test

import (
	"testing"

	"bkpr.pro/bkpr/lib/books"
)

// A description is what is being billed, and it labels the category leg. A taxed rent invoice books
// two legs whose accounts end in the same segment, so without it the document renders the same label
// twice; the description tells the two lines apart. The tax leg is left alone.
func TestADescriptionLabelsTheCategoryPosting(t *testing.T) {
	log := newLog()
	_, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(115000),
		Category:    "Income:Real Estate:Rent:Unit A",
		Description: "Rent for Aug 1",
		TaxRate:     "15%",
		TaxAccount:  "Liabilities:Real Estate:HST:Rent:Unit A",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}

	_, entries := booksOn(t, log, books.AccrualBasis)
	ps := postingsOf(t, entries, "Commercial Tenant")
	if len(ps) != 2 {
		t.Fatalf("got %d postings, want the income leg and the tax leg: %+v", len(ps), ps)
	}
	if ps[0].Comment != "Rent for Aug 1" {
		t.Errorf("income posting comment = %q, want the description", ps[0].Comment)
	}
	if ps[1].Comment != "" {
		t.Errorf("tax posting comment = %q, want it left unlabelled", ps[1].Comment)
	}
}

// An untaxed accrual labels its one category leg the same way, invoice and bill alike.
func TestADescriptionLabelsAnUntaxedAccrual(t *testing.T) {
	log := newLog()
	if _, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(100000),
		Category: "Income:Consulting", Description: "Consulting for Aug",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(1), Party: "A Vendor", Amount: cad(8420),
		Category: "Expenses:Repairs", Description: "Unit A furnace",
	}); err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}

	_, entries := booksOn(t, log, books.AccrualBasis)
	if got := postingsOf(t, entries, "Commercial Tenant"); got[0].Comment != "Consulting for Aug" {
		t.Errorf("invoice posting comment = %q, want the description", got[0].Comment)
	}
	if got := postingsOf(t, entries, "A Vendor"); got[0].Comment != "Unit A furnace" {
		t.Errorf("bill posting comment = %q, want the description", got[0].Comment)
	}
}

// The description rides through the log: it is part of the recorded fact, not a rendering choice, so
// a folded invoice or bill comes back holding it.
func TestDescriptionRoundTripsThroughTheLog(t *testing.T) {
	log := newLog()
	if _, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(115000),
		Category: "Income:Rent", Description: "Rent for Aug 1",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(1), Party: "A Vendor", Amount: cad(8420),
		Category: "Expenses:Repairs", Description: "Unit A furnace",
	}); err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}

	invs, err := books.Invoices(log)
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	if len(invs) != 1 || invs[0].Description != "Rent for Aug 1" {
		t.Fatalf("folded invoices = %+v, want the description kept", invs)
	}
	bills, err := books.Bills(log)
	if err != nil {
		t.Fatalf("Bills: %v", err)
	}
	if len(bills) != 1 || bills[0].Description != "Unit A furnace" {
		t.Fatalf("folded bills = %+v, want the description kept", bills)
	}
}

// The description is metadata about an invoice, exactly like its number: it describes the fact
// rather than being one, so re-describing an invoice is the same invoice and collapses to one fact.
func TestDescriptionIsNotPartOfTheFingerprint(t *testing.T) {
	log := newLog()
	base := books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(115000), Category: "Income:Rent",
	}
	first := base
	first.Description = "Rent for Aug 1"
	one, added, err := books.Raise(log, "human", "", first)
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if !added {
		t.Fatal("the first invoice was not recorded")
	}

	second := base
	second.Description = "August rent, Unit A"
	two, added, err := books.Raise(log, "human", "", second)
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if added {
		t.Error("a re-described invoice recorded a second fact")
	}
	if two.ID != one.ID {
		t.Errorf("fingerprints %q and %q differ; the description must not be hashed", one.ID, two.ID)
	}

	invs, err := books.Invoices(log)
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("folded invoices = %+v, want the one invoice", invs)
	}
}
