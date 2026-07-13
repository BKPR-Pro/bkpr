package books_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

// balanceOf folds the log into one account's current CAD balance, the figure accounts list shows.
func balanceOf(t *testing.T, log *eventlog.Log, account string) model.Amount {
	t.Helper()
	balances, err := books.Balances(log)
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	return balances[account]["CAD"]
}

// OBS-1: reconcile is a fold that records nothing, so voiding a line inside the reconciled window
// recomputes the delta rather than reading a stale one. A duplicate booked between the anchor and
// the latest bank figure shows as the delta; voiding it brings the account back to the penny.
func TestReconcileRecomputesAfterAVoidInsideTheWindow(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, 10000, "DEPOSIT")) // +100; anchor derives 400 opening
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(1), cad(50000))
	importOne(t, log, line("b", 5, 20000, "DEPOSIT"))      // +200, a real movement
	importOne(t, log, line("dup", 8, 5000, "DEPOSIT DUP")) // +50, a duplicate to void
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(10), cad(70000))

	// Before the void the duplicate is in the window: books 750 vs bank 700, off by the 50
	// (bank - books, so the books running high reads negative).
	if r := reconcileOne(t, log, "Assets:Bank:Chequing"); r.Delta.String() != "-50.00 CAD" {
		t.Fatalf("delta before void = %s, want -50.00 CAD (the duplicate)", r.Delta)
	}

	if err := books.VoidTransaction(log, "human", "duplicate of b", "dup"); err != nil {
		t.Fatalf("VoidTransaction: %v", err)
	}

	// The fold reruns: with the duplicate gone the account reconciles to the penny.
	if r := reconcileOne(t, log, "Assets:Bank:Chequing"); !r.Reconciled || !r.Delta.IsZero() {
		t.Fatalf("delta after void = %s, want 0 (reconcile recomputes, it does not cache)", r.Delta)
	}
}

// OBS-1, the other half: a line dated after the latest bank figure is outside the reconciled window,
// so voiding it moves the account's raw balance (accounts list, all dates) without touching the
// delta (folded to the assertion date). The two disagreeing is correct, not a stale delta.
func TestVoidAfterTheLastAssertionMovesTheBalanceNotTheDelta(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, 10000, "DEPOSIT")) // +100; anchor derives 400 opening
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(1), cad(50000))
	importOne(t, log, line("later", 20, 5000, "DEPOSIT")) // +50, dated after the latest assertion

	rawBefore := balanceOf(t, log, "Assets:Bank:Chequing")
	deltaBefore := reconcileOne(t, log, "Assets:Bank:Chequing").Delta

	if err := books.VoidTransaction(log, "human", "outside the window", "later"); err != nil {
		t.Fatalf("VoidTransaction: %v", err)
	}

	rawAfter := balanceOf(t, log, "Assets:Bank:Chequing")
	deltaAfter := reconcileOne(t, log, "Assets:Bank:Chequing").Delta

	if rawBefore.String() == rawAfter.String() {
		t.Errorf("raw balance did not move on the void: before=%s after=%s", rawBefore, rawAfter)
	}
	if deltaBefore.String() != deltaAfter.String() {
		t.Errorf("delta moved on an out-of-window void: before=%s after=%s", deltaBefore, deltaAfter)
	}
}
