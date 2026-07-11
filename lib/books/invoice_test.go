package books_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

// raise records a 1600.00 CAD invoice to a party on the given day, defaulting the parked account to
// Assets:Receivable.
func raise(t *testing.T, log *eventlog.Log, party string, day int, cents int64, category string) books.Invoice {
	t.Helper()
	inv, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(day), Party: party, Amount: cad(cents), Category: category,
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	return inv
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
// is the basis the tool was born on, and raising an invoice must not disturb it.
func TestAnInvoiceIsInvisibleOnTheCashBasis(t *testing.T) {
	log := newLog()
	raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")

	txs, _ := booksOn(t, log, books.CashBasis)
	if len(txs) != 0 {
		t.Fatalf("got %d cash-basis lines, want 0: an unpaid invoice is not cash", len(txs))
	}
}

// On the accrual basis the same invoice books as its own line: it debits the receivable and credits
// income, dated when the revenue was earned rather than when it will be paid.
func TestAnInvoiceBooksOnTheAccrualBasis(t *testing.T) {
	log := newLog()
	raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")

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
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	// The deposit lands in the bank and the rules would call it income.
	loaded(t, log, rule("j smith", "Income:Consulting"))
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

// A custom parked account rides through: an invoice may name where it holds until paid, so several
// customers can carry their own receivable sub-accounts.
func TestAnInvoiceMayNameItsReceivableAccount(t *testing.T) {
	log := newLog()
	inv, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "J. Smith", Amount: cad(160000),
		Category: "Income:Consulting", Account: "Assets:Receivable:J. Smith",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if inv.Account != "Assets:Receivable:J. Smith" {
		t.Fatalf("account = %q, want the one named", inv.Account)
	}

	txs, entries := booksOn(t, log, books.AccrualBasis)
	if got := balances(txs, entries)["Assets:Receivable:J. Smith"]; got != 160000 {
		t.Errorf("named receivable = %d, want 160000", got)
	}
}

// Voiding an invoice drops it from the books, the same operation as voiding a bad import. The raised
// fact stays in the log; the fold honours the later void.
func TestVoidingAnInvoiceDropsIt(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	if err := books.VoidInvoice(log, "human", "the client cancelled", inv.ID); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}

	if txs, _ := booksOn(t, log, books.AccrualBasis); len(txs) != 0 {
		t.Fatalf("got %d accrual-basis lines, want 0 after the void", len(txs))
	}
}

// Raising the same invoice twice records one fact, the way re-importing a statement is a no-op.
func TestRaisingTheSameInvoiceTwiceIsANoOp(t *testing.T) {
	log := newLog()
	first := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")

	again, added, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "J. Smith", Amount: cad(160000), Category: "Income:Consulting",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if added {
		t.Error("the second identical invoice was recorded again")
	}
	if again.ID != first.ID {
		t.Errorf("fingerprints differ: %q vs %q", again.ID, first.ID)
	}
	if invs, _ := books.Invoices(log); len(invs) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invs))
	}
}

// The seam property behind switching midstream: with no invoices raised, the accrual basis and the
// cash basis are identical. Accrual only ever adds the value it was told about, so history you never
// invoiced reads the same either way.
func TestWithNoInvoicesBothBasesAgree(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Repairs"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	importOne(t, log, line("b", 5, 160000, "E-TRANSFER FROM J SMITH"))

	cashTxs, _ := booksOn(t, log, books.CashBasis)
	accTxs, _ := booksOn(t, log, books.AccrualBasis)
	if len(cashTxs) != len(accTxs) {
		t.Fatalf("cash has %d lines, accrual has %d; they should agree with no invoices", len(cashTxs), len(accTxs))
	}
}

// Reopening a settled invoice unlinks it, so on the accrual basis the receivable no longer clears
// against that line.
func TestReopeningUnlinksTheSettlement(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	loaded(t, log, rule("j smith", "Income:Consulting"))
	importOne(t, log, line("pay", 20, 160000, "E-TRANSFER FROM J SMITH"))
	if err := books.Settle(log, "human", inv.ID, "pay"); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if err := books.Settle(log, "human", inv.ID, ""); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	if settled, _ := books.Settlements(log); len(settled) != 0 {
		t.Errorf("got %d settlements after reopening, want 0", len(settled))
	}
}

// Settling or voiding a fingerprint that was never raised is refused rather than recorded against
// nothing, the way categorizing an unimported line is.
func TestSettlingAnUnknownInvoiceIsRefused(t *testing.T) {
	log := newLog()
	if err := books.Settle(log, "human", "nope", ""); err == nil {
		t.Fatal("settled an invoice that was never raised")
	}
	if err := books.VoidInvoice(log, "human", "", "nope"); err == nil {
		t.Fatal("voided an invoice that was never raised")
	}
}
