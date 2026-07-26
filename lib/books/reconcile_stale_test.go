package books_test

import (
	"testing"

	"github.com/dallasread/bkpr/lib/books"
)

// An account whose connector stops reporting a balance keeps reconciling forever against whatever
// figure was recorded last, and says "reconciled" in exactly the words an account checked this morning
// uses. The AS OF column is the only difference, and it is easy to read past. Stale marks the case:
// the books have activity later than the last time this account was measured against the bank, so its
// verdict is about that older date and nothing since.
func TestReconcileMarksAnAnchorTheBooksHaveMovedPast(t *testing.T) {
	log := newLog()

	// The chequing is imported and anchored on the 20th -- the current cycle.
	importOne(t, log, lineIn("chq", "Assets:Bank:Chequing", 1, 10000, "DEPOSIT"))
	if err := books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(20), cad(10000)); err != nil {
		t.Fatalf("AssertBalance chequing: %v", err)
	}

	// The card was last measured on the 5th and never again, while the book kept moving.
	importOne(t, log, lineIn("card", "Liabilities:Card", 3, -2500, "CHARGE"))
	if err := books.AssertBalance(log, "rbc", "Liabilities:Card", on(5), cad(-2500)); err != nil {
		t.Fatalf("AssertBalance card: %v", err)
	}

	card := reconcileOne(t, log, "Liabilities:Card")
	if !card.Reconciled {
		t.Fatalf("the card should still reconcile against its own anchor, got delta %s", card.Delta)
	}
	if !card.Stale {
		t.Errorf("the card's anchor is dated %s while the books run to the 20th; want Stale",
			card.AsOf.Format("2006-01-02"))
	}

	chequing := reconcileOne(t, log, "Assets:Bank:Chequing")
	if chequing.Stale {
		t.Errorf("the chequing was anchored on the 20th, the newest date in the book; want not Stale")
	}
}

// Connectors in one cycle do not finish at the same moment -- one bank is read tonight, another
// tomorrow morning, and a balance-only account may be a day behind either. That ordinary drift must not
// read as an account nobody is checking, or the signal is noise on every run.
func TestReconcileAllowsOrdinaryDriftWithinACycle(t *testing.T) {
	log := newLog()

	importOne(t, log, lineIn("chq", "Assets:Bank:Chequing", 1, 10000, "DEPOSIT"))
	if err := books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(20), cad(10000)); err != nil {
		t.Fatalf("AssertBalance chequing: %v", err)
	}

	// Measured the day before the chequing, in the same cycle.
	importOne(t, log, lineIn("card", "Liabilities:Card", 3, -2500, "CHARGE"))
	if err := books.AssertBalance(log, "rbc", "Liabilities:Card", on(19), cad(-2500)); err != nil {
		t.Fatalf("AssertBalance card: %v", err)
	}

	if card := reconcileOne(t, log, "Liabilities:Card"); card.Stale {
		t.Errorf("an anchor one day behind the newest is ordinary cycle drift; want not Stale")
	}
}
