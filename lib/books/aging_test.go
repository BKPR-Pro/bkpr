package books_test

import (
	"testing"
	"time"

	"github.com/dallasread/bkpr/lib/books"
)

func asOf(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Invoice aging places each open receivable in a bucket by how long it has been owed, oldest first.
func TestInvoiceAgingBucketsOpenReceivables(t *testing.T) {
	log := newLog()
	// Raise invoices at spread-out dates, then age them as of a fixed day.
	raise(t, log, "Fresh", 1, 100, "Income:Consulting") // Mar 1
	raise(t, log, "Old", 1, 200, "Income:Consulting")   // also Mar 1 but we'll rely on age
	as := asOf(2026, 3, 20)                             // 19 days after Mar 1 -> current

	rows, err := books.InvoiceAging(log, as)
	if err != nil {
		t.Fatalf("InvoiceAging: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d aged rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Days != 19 || r.Bucket != "current" {
			t.Errorf("%s: days=%d bucket=%q, want 19/current", r.Party, r.Days, r.Bucket)
		}
	}
}

// The 30/60/90 bands are what an accountant expects, so age is bucketed at those boundaries.
func TestAgingBucketBoundaries(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "Client", 1, 100, "Income:Consulting") // Mar 1
	_ = inv

	cases := []struct {
		as     time.Time
		bucket string
	}{
		{asOf(2026, 3, 31), "current"}, // 30 days
		{asOf(2026, 4, 1), "31-60"},    // 31 days
		{asOf(2026, 4, 30), "31-60"},   // 60 days
		{asOf(2026, 5, 1), "61-90"},    // 61 days
		{asOf(2026, 5, 30), "61-90"},   // 90 days
		{asOf(2026, 5, 31), "90+"},     // 91 days
		{asOf(2026, 7, 1), "90+"},      // well past 90
	}
	for _, c := range cases {
		rows, _ := books.InvoiceAging(log, c.as)
		if len(rows) != 1 {
			t.Fatalf("as of %s: got %d rows", c.as.Format("2006-01-02"), len(rows))
		}
		if rows[0].Bucket != c.bucket {
			t.Errorf("as of %s: days=%d bucket=%q, want %q", c.as.Format("2006-01-02"), rows[0].Days, rows[0].Bucket, c.bucket)
		}
	}
}

// A settled invoice no longer ages: only what is still owed appears in the report.
func TestSettledInvoicesDoNotAge(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	importOne(t, log, line("pay", 20, 160000, "E-TRANSFER FROM J SMITH"))
	if err := books.SettleInvoice(log, "human", inv.ID, "pay"); err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}

	rows, err := books.InvoiceAging(log, asOf(2026, 4, 1))
	if err != nil {
		t.Fatalf("InvoiceAging: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d aged rows, want 0: a paid invoice is not a receivable", len(rows))
	}
}

// Bill aging shows the payable's magnitude (a positive amount owed) even though a payable is held
// negative internally.
func TestBillAgingShowsMagnitudeOwed(t *testing.T) {
	log := newLog()
	receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")

	rows, err := books.BillAging(log, asOf(2026, 3, 15))
	if err != nil {
		t.Fatalf("BillAging: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Amount.String() != "500.00 CAD" {
		t.Errorf("amount = %q, want 500.00 CAD (magnitude owed)", rows[0].Amount.String())
	}
}
