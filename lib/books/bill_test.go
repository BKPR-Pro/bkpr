package books_test

import (
	"testing"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/eventlog"
)

// receive records a bill to a vendor on the given day, defaulting the parked account to
// Liabilities:Payable.
func receive(t *testing.T, log *eventlog.Log, party string, day int, cents int64, category string) books.Bill {
	t.Helper()
	b, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(day), Party: party, Amount: cad(cents), Category: category,
	})
	if err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}
	return b
}

// On the cash basis a bill is invisible, exactly like an unpaid invoice: nothing has left yet.
func TestABillIsInvisibleOnTheCashBasis(t *testing.T) {
	log := newLog()
	receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")

	if txs, _ := booksOn(t, log, books.CashBasis); len(txs) != 0 {
		t.Fatalf("got %d cash-basis lines, want 0: an unpaid bill is not cash", len(txs))
	}
}

// On the accrual basis a bill books its own line: it debits the expense and credits the payable,
// dated when the expense was incurred rather than when it will be paid.
func TestABillBooksAPayableOnTheAccrualBasis(t *testing.T) {
	log := newLog()
	receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")

	txs, entries := booksOn(t, log, books.AccrualBasis)
	if len(txs) != 1 {
		t.Fatalf("got %d accrual-basis lines, want 1", len(txs))
	}
	bal := balances(txs, entries)
	if bal["Expenses:Utilities:Power"] != 50000 {
		t.Errorf("expense = %d, want 50000 debited", bal["Expenses:Utilities:Power"])
	}
	if bal["Liabilities:Payable"] != -50000 {
		t.Errorf("payable = %d, want -50000 credited", bal["Liabilities:Payable"])
	}
}

// The payment that settles a bill clears the payable instead of booking the expense a second time.
// The expense is recognized once (at the bill), and the payable nets to zero once the cash leaves.
func TestSettlingABillClearsThePayableWithoutDoubleBookingExpense(t *testing.T) {
	log := newLog()
	b := receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")
	// The payment leaves the bank and the rules would call it an expense.
	loaded(t, log, rule("power co", "Expenses:Utilities:Power"))
	importOne(t, log, line("pay", 20, -50000, "POWER CO PREAUTH"))
	if err := books.SettleBill(log, "human", b.ID, "pay"); err != nil {
		t.Fatalf("SettleBill: %v", err)
	}

	txs, entries := booksOn(t, log, books.AccrualBasis)
	bal := balances(txs, entries)
	if bal["Liabilities:Payable"] != 0 {
		t.Errorf("payable = %d, want 0 after the bill was paid", bal["Liabilities:Payable"])
	}
	if bal["Expenses:Utilities:Power"] != 50000 {
		t.Errorf("expense = %d, want 50000 booked exactly once", bal["Expenses:Utilities:Power"])
	}
	for i, tx := range txs {
		if tx.ID == "pay" && entries[i].Postings[0].Account != "Liabilities:Payable" {
			t.Errorf("the paying line posts to %q, want Liabilities:Payable", entries[i].Postings[0].Account)
		}
	}
}

// Voiding a bill drops it from the books, the same operation as voiding an invoice or a bad import.
func TestVoidingABillDropsIt(t *testing.T) {
	log := newLog()
	b := receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")
	if err := books.VoidBill(log, "human", "billed in error", b.ID); err != nil {
		t.Fatalf("VoidBill: %v", err)
	}
	if txs, _ := booksOn(t, log, books.AccrualBasis); len(txs) != 0 {
		t.Fatalf("got %d accrual-basis lines, want 0 after the void", len(txs))
	}
}

// Receiving the same bill twice records one fact, the way re-importing a statement is a no-op.
func TestReceivingTheSameBillTwiceIsANoOp(t *testing.T) {
	log := newLog()
	first := receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")
	again, added, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(1), Party: "Power Co", Amount: cad(50000), Category: "Expenses:Utilities:Power",
	})
	if err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}
	if added {
		t.Error("the second identical bill was recorded again")
	}
	if again.ID != first.ID {
		t.Errorf("fingerprints differ: %q vs %q", again.ID, first.ID)
	}
	if bills, _ := books.Bills(log); len(bills) != 1 {
		t.Fatalf("got %d bills, want 1", len(bills))
	}
}

// Invoices and bills coexist on one accrual-basis rendering: a receivable and a payable book side by
// side, and the shared overlay keeps them in date order.
func TestInvoicesAndBillsCoexistOnTheAccrualBasis(t *testing.T) {
	log := newLog()
	raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	receive(t, log, "Power Co", 2, 50000, "Expenses:Utilities:Power")

	txs, entries := booksOn(t, log, books.AccrualBasis)
	if len(txs) != 2 {
		t.Fatalf("got %d accrual-basis lines, want 2 (one invoice, one bill)", len(txs))
	}
	bal := balances(txs, entries)
	if bal["Assets:Receivable"] != 160000 {
		t.Errorf("receivable = %d, want 160000", bal["Assets:Receivable"])
	}
	if bal["Liabilities:Payable"] != -50000 {
		t.Errorf("payable = %d, want -50000", bal["Liabilities:Payable"])
	}
}
