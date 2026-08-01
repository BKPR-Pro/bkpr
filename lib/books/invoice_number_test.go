package books_test

import (
	"testing"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/eventlog"
)

// next folds the log for the number that would be issued now.
func next(t *testing.T, log *eventlog.Log) string {
	t.Helper()
	n, err := books.NextInvoiceNumber(log)
	if err != nil {
		t.Fatalf("NextInvoiceNumber: %v", err)
	}
	return n
}

// A book that has never numbered anything starts at one, so the first invoice out the door needs no
// decision from the person raising it.
func TestNextInvoiceNumberStartsAtOne(t *testing.T) {
	if got := next(t, newLog()); got != "1" {
		t.Errorf("next = %q, want 1", got)
	}
}

// A number lives in two places -- on a raised accrual and on a categorized bank line -- and both are
// the same sequence, so the next one is past the highest of either.
func TestNextInvoiceNumberReadsAccrualsAndCategorizations(t *testing.T) {
	log := newLog()
	if _, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(160000),
		Category: "Income:Rent", Number: "2085",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if got := next(t, log); got != "2086" {
		t.Errorf("next = %q, want 2086 from the raised accrual", got)
	}

	importOne(t, log, line("a", 2, 160000, "DEPOSIT"))
	if err := books.Categorize(log, "human", "", "a", "2090", "Commercial Tenant", "", whole("Income:Rent", 160000)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if got := next(t, log); got != "2091" {
		t.Errorf("next = %q, want 2091 from the categorized line", got)
	}
}

// A gap is never filled. The missing number may already be printed on a document that was withheld
// or voided, so reusing it could collide with paper already sent: the sequence only ever climbs.
func TestNextInvoiceNumberNeverFillsAGap(t *testing.T) {
	log := newLog()
	for _, n := range []string{"2020", "2022", "2024"} { // 2021 and 2023 are missing
		if _, _, err := books.Raise(log, "human", "", books.Invoice{
			Date: on(1), Party: "Commercial Tenant " + n, Amount: cad(160000),
			Category: "Income:Rent", Number: n,
		}); err != nil {
			t.Fatalf("Raise: %v", err)
		}
	}
	if got := next(t, log); got != "2025" {
		t.Errorf("next = %q, want 2025 -- past the highest, not into a gap", got)
	}
}

// A vendor's own numbering is not our sequence, so anything that is not entirely digits is ignored
// rather than parsed for the digits it happens to contain.
func TestNextInvoiceNumberIgnoresNonNumericNumbers(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -160000, "A VENDOR"))
	if err := books.Categorize(log, "human", "", "a", "INV-7c", "A Vendor", "", whole("Expenses:Supplies", -160000)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if got := next(t, log); got != "1" {
		t.Errorf("next = %q, want 1 -- a vendor's number is not part of our sequence", got)
	}
}

// Voiding an invoice does not release its number: the document may have gone out before the void, so
// the number stays spent and the next one is still past it.
func TestNextInvoiceNumberCountsAVoidedInvoice(t *testing.T) {
	log := newLog()
	inv, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(160000),
		Category: "Income:Rent", Number: "2085",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if err := books.VoidInvoice(log, "human", "raised in error", inv.ID); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}
	if got := next(t, log); got != "2086" {
		t.Errorf("next = %q, want 2086 -- a voided number is still spent", got)
	}
}
