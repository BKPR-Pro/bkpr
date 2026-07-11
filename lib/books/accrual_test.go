package books_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

// invoice recognizes a 1600.00 CAD invoice to a party on the given day, defaulting the parked
// account to Assets:Receivable.
func invoice(t *testing.T, log *eventlog.Log, party string, day int, cents int64, category string) books.Accrual {
	t.Helper()
	a, _, err := books.Recognize(log, "human", "", books.Accrual{
		Kind: books.KindInvoice, Date: on(day), Party: party, Amount: cad(cents), Category: category,
	})
	if err != nil {
		t.Fatalf("Recognize invoice: %v", err)
	}
	return a
}

// booksOn folds the log through the given basis.
func booksOn(t *testing.T, log *eventlog.Log, basis books.Basis) ([]model.Transaction, []model.Entry) {
	t.Helper()
	txs, entries, err := books.LedgerBasis(log, basis)
	if err != nil {
		t.Fatalf("LedgerBasis(%s): %v", basis, err)
	}
	return txs, entries
}

// balances sums every posting, including the elided source-account posting of each line (its amount
// is the line's own amount), into a per-account total. It is how a test asks what the books say a
// receivable is worth after some events.
func balances(txs []model.Transaction, entries []model.Entry) map[string]int64 {
	out := map[string]int64{}
	for i, tx := range txs {
		for _, p := range entries[i].Postings {
			out[p.Account] += p.Amount.Units
		}
		out[tx.Account] += tx.Amount.Units
	}
	return out
}

// On the cash basis, an invoice is invisible: nothing moved yet, so there is nothing to book. This
// is the basis the tool was born on, and recognizing an accrual must not disturb it.
func TestAnInvoiceIsInvisibleOnTheCashBasis(t *testing.T) {
	log := newLog()
	invoice(t, log, "J. Smith", 1, 160000, "Income:Consulting")

	txs, _ := booksOn(t, log, books.CashBasis)
	if len(txs) != 0 {
		t.Fatalf("got %d cash-basis lines, want 0: an unpaid invoice is not cash", len(txs))
	}
}

// On the accrual basis the same invoice books as its own line: it debits the receivable and credits
// income, dated when the value was earned rather than when it will be paid.
func TestAnInvoiceBooksOnTheAccrualBasis(t *testing.T) {
	log := newLog()
	invoice(t, log, "J. Smith", 1, 160000, "Income:Consulting")

	txs, entries := booksOn(t, log, books.AccrualBasis)
	if len(txs) != 1 {
		t.Fatalf("got %d accrual-basis lines, want 1", len(txs))
	}
	if got := entries[0].Postings[0]; got.Account != "Income:Consulting" || got.Amount.String() != "-1600.00 CAD" {
		t.Errorf("income posting = %+v, want a -1600.00 CAD credit to Income:Consulting", got)
	}
	if got := balances(txs, entries)["Assets:Receivable"]; got != 160000 {
		t.Errorf("receivable = %d, want 160000 debited", got)
	}
}

// The whole point of settlement: the deposit that pays an invoice clears the receivable instead of
// booking income a second time. Income is recognized once (at the invoice), and the receivable nets
// to zero once the cash arrives.
func TestSettlementClearsTheReceivableWithoutDoubleBookingIncome(t *testing.T) {
	log := newLog()
	inv := invoice(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	// The rent/deposit lands in the bank and the rules would call it income.
	loaded(t, log, rule("j. smith", "Income:Consulting"))
	importOne(t, log, line("pay", 20, 160000, "E-TRANSFER FROM J SMITH"))
	if err := books.Settle(log, "human", inv.ID, "pay"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	txs, entries := booksOn(t, log, books.AccrualBasis)
	bal := balances(txs, entries)
	if bal["Assets:Receivable"] != 0 {
		t.Errorf("receivable = %d, want 0 after the invoice was paid", bal["Assets:Receivable"])
	}
	if bal["Income:Consulting"] != -160000 {
		t.Errorf("income = %d, want -160000 booked exactly once", bal["Income:Consulting"])
	}
	// The settling line must post to the receivable, not to income again.
	for i, tx := range txs {
		if tx.ID == "pay" && entries[i].Postings[0].Account != "Assets:Receivable" {
			t.Errorf("the paying line posts to %q, want Assets:Receivable", entries[i].Postings[0].Account)
		}
	}
}

// A bill is the mirror of an invoice: on the accrual basis it debits an expense and credits a
// payable, so the expense is recognized when incurred rather than when the cash leaves.
func TestABillBooksAPayableOnTheAccrualBasis(t *testing.T) {
	log := newLog()
	if _, _, err := books.Recognize(log, "human", "", books.Accrual{
		Kind: books.KindBill, Date: on(1), Party: "Power Co", Amount: cad(50000), Category: "Expenses:Utilities",
	}); err != nil {
		t.Fatalf("Recognize bill: %v", err)
	}

	txs, entries := booksOn(t, log, books.AccrualBasis)
	bal := balances(txs, entries)
	if bal["Expenses:Utilities"] != 50000 {
		t.Errorf("expense = %d, want 50000 debited", bal["Expenses:Utilities"])
	}
	if bal["Liabilities:Payable"] != -50000 {
		t.Errorf("payable = %d, want -50000 credited", bal["Liabilities:Payable"])
	}
}

// Voiding an accrual drops it from the books, the way discard drops a bad import. The recognized
// fact stays in the log; the fold honours the later void.
func TestVoidingAnAccrualDropsIt(t *testing.T) {
	log := newLog()
	inv := invoice(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	if err := books.Void(log, "human", "the client cancelled", inv.ID); err != nil {
		t.Fatalf("Void: %v", err)
	}

	if txs, _ := booksOn(t, log, books.AccrualBasis); len(txs) != 0 {
		t.Fatalf("got %d accrual-basis lines, want 0 after the void", len(txs))
	}
}

// Recognizing the same invoice twice records one fact, the way re-importing a statement is a no-op.
func TestRecognizingTheSameInvoiceTwiceIsANoOp(t *testing.T) {
	log := newLog()
	first := invoice(t, log, "J. Smith", 1, 160000, "Income:Consulting")

	again, added, err := books.Recognize(log, "human", "", books.Accrual{
		Kind: books.KindInvoice, Date: on(1), Party: "J. Smith", Amount: cad(160000), Category: "Income:Consulting",
	})
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if added {
		t.Error("the second identical invoice was recorded again")
	}
	if again.ID != first.ID {
		t.Errorf("fingerprints differ: %q vs %q", again.ID, first.ID)
	}
	if accs, _ := books.Accruals(log); len(accs) != 1 {
		t.Fatalf("got %d accruals, want 1", len(accs))
	}
}

// The seam property behind switching midstream: with no accruals recognized, the accrual basis and
// the cash basis are identical. Accrual only ever adds the value it was told about, so history you
// never invoiced reads the same either way.
func TestWithNoAccrualsBothBasesAgree(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Repairs"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	importOne(t, log, line("b", 5, 160000, "E-TRANSFER FROM J SMITH"))

	cashTxs, _ := booksOn(t, log, books.CashBasis)
	accTxs, _ := booksOn(t, log, books.AccrualBasis)
	if len(cashTxs) != len(accTxs) {
		t.Fatalf("cash has %d lines, accrual has %d; they should agree with no accruals", len(cashTxs), len(accTxs))
	}
}

// Settling or voiding a fingerprint that was never recognized is refused rather than recorded
// against nothing, the way categorizing an unimported line is.
func TestSettlingAnUnknownAccrualIsRefused(t *testing.T) {
	log := newLog()
	if err := books.Settle(log, "human", "nope", ""); err == nil {
		t.Fatal("settled an accrual that was never recognized")
	}
	if err := books.Void(log, "human", "", "nope"); err == nil {
		t.Fatal("voided an accrual that was never recognized")
	}
}
