package books_test

import (
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

func accrualSince(t *testing.T, log *eventlog.Log, since time.Time) ([]model.Transaction, []model.Entry) {
	t.Helper()
	txs, entries, err := books.LedgerBasisSince(log, books.AccrualBasis, since)
	if err != nil {
		t.Fatalf("LedgerBasisSince: %v", err)
	}
	return txs, entries
}

// An effective date bounds the switch: an invoice dated before it is not accrued, so those books
// read exactly as they did on cash. This is what makes turning on accrual mid-year safe.
func TestSinceExcludesInvoicesBeforeTheEffectiveDate(t *testing.T) {
	log := newLog()
	raise(t, log, "Old Client", 1, 160000, "Income:Consulting") // March 1
	raise(t, log, "New Client", 10, 90000, "Income:Consulting") // March 10

	txs, _ := accrualSince(t, log, on(5)) // switch effective March 5
	if len(txs) != 1 {
		t.Fatalf("got %d accrued lines, want 1: only the invoice on/after the effective date", len(txs))
	}
	if txs[0].Description != "New Client" {
		t.Errorf("accrued %q, want the New Client invoice", txs[0].Description)
	}
}

// An invoice dated on the effective date itself is included: the cutoff is inclusive, so the day you
// switch is the first day accrual applies.
func TestSinceIsInclusiveOfTheEffectiveDate(t *testing.T) {
	log := newLog()
	raise(t, log, "Client", 5, 160000, "Income:Consulting")

	if txs, _ := accrualSince(t, log, on(5)); len(txs) != 1 {
		t.Fatalf("got %d lines, want 1: an invoice dated on the effective date is accrued", len(txs))
	}
}

// A receivable that predates the switch reads as cash: its settling deposit is not redirected to the
// receivable but books as ordinary income when it lands, because the invoice was never accrued.
func TestSinceLeavesAPreDateSettlementAsCashIncome(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting") // before the switch
	loaded(t, log, rule("j smith", "Income:Consulting"))
	importOne(t, log, line("pay", 20, 160000, "E-TRANSFER FROM J SMITH"))
	if err := books.SettleInvoice(log, "human", inv.ID, "pay"); err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}

	txs, entries := accrualSince(t, log, on(10)) // switch after the invoice, before the payment
	bal := balances(txs, entries)
	if bal["Assets:Receivable"] != 0 {
		t.Errorf("receivable = %d, want 0: a pre-date invoice is never booked", bal["Assets:Receivable"])
	}
	if bal["Income:Consulting"] != -160000 {
		t.Errorf("income = %d, want -160000 booked once, as cash when the deposit landed", bal["Income:Consulting"])
	}
	// The paying line books as income, not as a receivable clearance.
	for i, tx := range txs {
		if tx.ID == "pay" && entries[i].Postings[0].Account != "Income:Consulting" {
			t.Errorf("the paying line posts to %q, want Income:Consulting (cash)", entries[i].Postings[0].Account)
		}
	}
}

// A zero effective date books every accrual, so LedgerBasis and a zero-since accrual read the same.
func TestZeroSinceBooksEveryAccrual(t *testing.T) {
	log := newLog()
	raise(t, log, "A", 1, 100, "Income:Consulting")
	raise(t, log, "B", 10, 200, "Income:Consulting")

	plain, _ := booksOn(t, log, books.AccrualBasis)
	zero, _ := accrualSince(t, log, time.Time{})
	if len(plain) != len(zero) || len(zero) != 2 {
		t.Fatalf("zero-since should book all accruals: plain=%d zero=%d", len(plain), len(zero))
	}
}
