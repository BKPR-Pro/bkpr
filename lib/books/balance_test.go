package books_test

import (
	"testing"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/eventlog"
)

func reconcileOne(t *testing.T, log *eventlog.Log, account string) books.Reconciliation {
	t.Helper()
	recs, err := books.Reconcile(log)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, r := range recs {
		if r.Account == account {
			return r
		}
	}
	t.Fatalf("no reconciliation for %s", account)
	return books.Reconciliation{}
}

// The first bank figure anchors the account: the imports do not reach its opening balance, so the
// anchor derives it and the account reconciles by definition at that point.
func TestFirstAssertionAnchorsAndReconciles(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, 10000, "DEPOSIT")) // +100 into chequing; books there are +100
	if err := books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(1), cad(50000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}

	r := reconcileOne(t, log, "Assets:Bank:Chequing")
	if !r.Reconciled {
		t.Fatalf("the first assertion should anchor and reconcile, got delta %s", r.Delta)
	}
	if r.Bank.String() != "500.00 CAD" || r.Books.String() != "500.00 CAD" {
		t.Errorf("bank=%s books=%s, want both 500.00 CAD (400 opening + 100 imported)", r.Bank, r.Books)
	}
}

// Once anchored, a later bank figure is a real check: when every movement since was captured, the
// books fold to exactly the bank's number.
func TestLaterAssertionReconcilesWhenNothingIsMissed(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, 10000, "DEPOSIT")) // +100
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(1), cad(50000))
	importOne(t, log, line("b", 10, -3000, "WITHDRAW")) // -30, so books move to +70 (bank 470)
	if err := books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(10), cad(47000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}

	r := reconcileOne(t, log, "Assets:Bank:Chequing")
	if !r.Reconciled || r.Delta.String() != "0.00 CAD" {
		t.Fatalf("should reconcile to the penny, got books=%s bank=%s delta=%s", r.Books, r.Bank, r.Delta)
	}
}

// A movement the bank saw but the books did not surfaces as exactly its size: the delta is the gap.
func TestAMissedMovementShowsAsTheDelta(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, 10000, "DEPOSIT"))
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(1), cad(50000))
	// No second line imported, but the bank says the balance rose by 25.00 -- a movement we missed.
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(10), cad(52500))

	r := reconcileOne(t, log, "Assets:Bank:Chequing")
	if r.Reconciled {
		t.Fatal("a missed movement should not reconcile")
	}
	if r.Delta.String() != "25.00 CAD" {
		t.Errorf("delta = %s, want 25.00 CAD (the missing movement)", r.Delta)
	}
}

// A transfer booked from the far side lands in the counter account as a posting, so that account's
// reconciled balance still reflects the money -- even though its own sighting was suppressed.
func TestReconcileCountsATransferBookedFromTheOtherSide(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -50000, "E-TRANSFER"))                         // chequing -500
	importOne(t, log, lineIn("b", "Assets:Bank:Savings", 1, 50000, "E-TRANSFER")) // savings +500 (suppressed)
	// Savings holds 500 from the transfer; anchor it there and it reconciles.
	if err := books.AssertBalance(log, "rbc", "Assets:Bank:Savings", on(1), cad(50000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}
	r := reconcileOne(t, log, "Assets:Bank:Savings")
	if r.Books.String() != "500.00 CAD" {
		t.Errorf("savings books = %s, want 500.00 CAD from the transfer posting", r.Books)
	}
}
