package books_test

import (
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
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

// An account can be finished: a connector re-pointed away from it, or its lines collapsed onto the
// account they belonged to. What it must not do is go on reporting. Its first assertion derives the
// opening balance, so it matches by construction no matter what it now holds -- an emptied slice kept
// printing "-16,515.19  0 (reconciled)" with no lines in it at all. Asserting zero cannot fix that: the
// first anchor stays first and the derived offset stays, so the zero reads as a delta. Retiring drops
// the account from the check entirely, leaving its history untouched.
func TestRetiringAnAccountDropsItFromReconcile(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("card", "Liabilities:Card:Slice", 3, -2500, "CHARGE"))
	if err := books.AssertBalance(log, "rbc", "Liabilities:Card:Slice", on(5), cad(-2500)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}
	if reconcileOne(t, log, "Liabilities:Card:Slice").Account == "" {
		t.Fatal("the account should be reconciling before it is retired")
	}

	if err := books.RetireBalance(log, "human", "Liabilities:Card:Slice"); err != nil {
		t.Fatalf("RetireBalance: %v", err)
	}

	recs, err := books.Reconcile(log)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, r := range recs {
		if r.Account == "Liabilities:Card:Slice" {
			t.Errorf("the retired account still reports: %+v", r)
		}
	}
}

// Retiring is not deletion: the account's own history stays, and a bank figure recorded afterwards
// starts the account over -- that later assertion anchors it, and the figures from before the retire
// do not come back to derive an offset against.
func TestABalanceAfterRetiringReanchorsTheAccount(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("card", "Liabilities:Card:Slice", 3, -2500, "CHARGE"))
	books.AssertBalance(log, "rbc", "Liabilities:Card:Slice", on(5), cad(-2500))
	if err := books.RetireBalance(log, "human", "Liabilities:Card:Slice"); err != nil {
		t.Fatalf("RetireBalance: %v", err)
	}

	if err := books.AssertBalance(log, "rbc", "Liabilities:Card:Slice", on(20), cad(-9999)); err != nil {
		t.Fatalf("AssertBalance after retiring: %v", err)
	}

	r := reconcileOne(t, log, "Liabilities:Card:Slice")
	if !r.Reconciled {
		t.Errorf("the re-anchored account should reconcile by construction, got delta %s", r.Delta)
	}
	if r.Bank.String() != "-99.99 CAD" {
		t.Errorf("bank = %s, want the assertion made after the retire", r.Bank)
	}
	if !r.AsOf.Equal(on(20)) {
		t.Errorf("as-of = %s, want the later assertion's date", r.AsOf.Format("2006-01-02"))
	}
}

// "Reconciled" names a narrower claim than it reads as. The first assertion for an account derives the
// opening balance, so the account matches at that date by construction -- every line dated on or before
// it sits inside the derived offset and no error among them can ever show a delta. On the real books
// the chequing's first anchor was dated 2026-07-14, and when an import re-served five 2026-07-14 lines
// under fuller memos, $702.03 was double-counted on the anchor date itself and reconcile went on
// reporting 0 to the penny -- correctly by its own definition, and uselessly. Say which window the
// verdict covers, so the anchored part is visible as anchored rather than checked.
func TestReconcileReportsTheWindowItActuallyChecks(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Assets:Bank:Chequing", 1, 10000, "DEPOSIT"))
	books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(5), cad(50000))
	importOne(t, log, lineIn("b", "Assets:Bank:Chequing", 10, -3000, "WITHDRAW"))
	if err := books.AssertBalance(log, "rbc", "Assets:Bank:Chequing", on(12), cad(47000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}

	r := reconcileOne(t, log, "Assets:Bank:Chequing")
	if !r.Since.Equal(on(5)) {
		t.Errorf("since = %s, want the first anchor's date -- everything before it is derived, not checked",
			r.Since.Format("2006-01-02"))
	}
	if r.Anchored {
		t.Errorf("an account with a later assertion has been checked against the bank; want Anchored false")
	}
}

// An account carrying a single assertion has never been checked at all: that one figure defined its
// opening balance, so it agrees by construction and would agree whatever the account held. It reports
// exactly like an account measured twice and found correct, which is the confusion worth ending -- the
// PC Mastercard sat that way while it was 6,032.21 short of its statement.
func TestReconcileMarksAnAccountThatOnlyAnchored(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:Card", 3, -2500, "CHARGE"))
	if err := books.AssertBalance(log, "rbc", "Liabilities:Card", on(5), cad(-9999)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}

	r := reconcileOne(t, log, "Liabilities:Card")
	if !r.Reconciled {
		t.Fatalf("a first anchor makes the account agree by construction, got delta %s", r.Delta)
	}
	if !r.Anchored {
		t.Errorf("one assertion is an anchor, not a check; want Anchored true")
	}
}
