package books_test

import (
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
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
	if err := books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(1), cad(50000)); err != nil {
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
	books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(1), cad(50000))
	importOne(t, log, line("b", 10, -3000, "WITHDRAW")) // -30, so books move to +70 (bank 470)
	if err := books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(10), cad(47000)); err != nil {
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
	books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(1), cad(50000))
	// No second line imported, but the bank says the balance rose by 25.00 -- a movement we missed.
	books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(10), cad(52500))

	r := reconcileOne(t, log, "Assets:Bank:Chequing")
	if r.Reconciled {
		t.Fatal("a missed movement should not reconcile")
	}
	if r.Delta.String() != "25.00 CAD" {
		t.Errorf("delta = %s, want 25.00 CAD (the missing movement)", r.Delta)
	}
}

// The figure accounts due/list show must be the same one reconcile computes -- an anchor offset plus
// what has moved since -- not a raw lifetime sum. A backfill importing history the anchor never
// covered drifts the raw sum off the true balance while the anchored figure stays correct throughout.
func TestReconciledBalancesMatchesReconcileNotARawSum(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:Acme Card", 5, -10000, "CHARGE")) // -100, opening balance not in the ledger
	if err := books.AssertBalance(log, "human", "Liabilities:Acme Card", on(5), cad(-320000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}
	// A later backfill lands history the anchor never covered.
	importOne(t, log, lineIn("b", "Liabilities:Acme Card", 1, -50000, "BACKFILLED CHARGE"))

	want := reconcileOne(t, log, "Liabilities:Acme Card").Books

	balances, err := books.ReconciledBalances(log)
	if err != nil {
		t.Fatalf("ReconciledBalances: %v", err)
	}
	got := balances["Liabilities:Acme Card"]["CAD"]
	if got.String() != want.String() {
		t.Errorf("balance = %s, want %s (reconcile's Books figure)", got, want)
	}
	rawSum := cad(-10000 + -50000)
	if got.String() == rawSum.String() {
		t.Fatalf("balance should not be the raw lifetime sum %s", rawSum)
	}
}

// ReconcileHistory checks every bank-asserted balance for an account against the books, in order, so a
// gap can be bisected to the assertion that first introduced it -- not just the latest one.
func TestReconcileHistoryFindsWhenADeltaFirstAppears(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, 10000, "DEPOSIT")) // +100
	books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(1), cad(50000))
	importOne(t, log, line("b", 10, -3000, "WITHDRAW")) // -30
	books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(10), cad(47000))
	// A movement between day 10 and day 20 the books never captured: the bank jumps by 25.00 with
	// nothing imported to explain it.
	books.AssertBalance(log, "acme", "Assets:Bank:Chequing", on(20), cad(49500))

	hist, err := books.ReconcileHistory(log, "Assets:Bank:Chequing")
	if err != nil {
		t.Fatalf("ReconcileHistory: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("got %d rows, want 3 (one per assertion)", len(hist))
	}
	for i, want := range []struct {
		asOf       time.Time
		reconciled bool
	}{
		{on(1), true},
		{on(10), true},
		{on(20), false},
	} {
		if !hist[i].AsOf.Equal(want.asOf) {
			t.Errorf("row %d: AsOf = %s, want %s", i, hist[i].AsOf, want.asOf)
		}
		if hist[i].Reconciled != want.reconciled {
			t.Errorf("row %d (as of %s): reconciled = %v, want %v (delta %s)", i, hist[i].AsOf, hist[i].Reconciled, want.reconciled, hist[i].Delta)
		}
	}
	if hist[2].Delta.String() != "25.00 CAD" {
		t.Errorf("first divergent row delta = %s, want 25.00 CAD", hist[2].Delta)
	}
}

// A transfer booked from the far side lands in the counter account as a posting, so that account's
// reconciled balance still reflects the money -- even though its own sighting was suppressed.
func TestReconcileCountsATransferBookedFromTheOtherSide(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -50000, "E-TRANSFER"))                         // chequing -500
	importOne(t, log, lineIn("b", "Assets:Bank:Savings", 1, 50000, "E-TRANSFER")) // savings +500 (suppressed)
	// Savings holds 500 from the transfer; anchor it there and it reconciles.
	if err := books.AssertBalance(log, "acme", "Assets:Bank:Savings", on(1), cad(50000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}
	r := reconcileOne(t, log, "Assets:Bank:Savings")
	if r.Books.String() != "500.00 CAD" {
		t.Errorf("savings books = %s, want 500.00 CAD from the transfer posting", r.Books)
	}
}
