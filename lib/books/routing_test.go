package books_test

import (
	"testing"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/rules"
)

// A physical card imported on one registered account is split by purpose: a categorization routes
// the card leg to a purpose sub-account, so the charge lands on the child while the offset lands on
// the expense. The registered parent keeps none of it directly, so its subtree still ties to the one
// bank balance (children roll up to the parent in reconcile).
func TestCategorizeRoutesTheCardLegToASubAccount(t *testing.T) {
	log := newLog()
	// A charge imported on the registered card account: money out (-100) raises the liability.
	importOne(t, log, lineIn("k", "Liabilities:PC Mastercard", 3, -10000, "KENT BUILDING SUPPLIES"))

	err := books.Categorize(log, "human", "Unit 1 reno", "k", "", "Kent",
		"Liabilities:PC Mastercard:9 Schoodic Street",
		whole("Expenses:Real Estate:Materials:9 Schoodic Street", -10000))
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "k").Source; got != "Liabilities:PC Mastercard:9 Schoodic Street" {
		t.Errorf("entry Source = %q, want the routed child", got)
	}
	if got := balanceOf(t, log, "Liabilities:PC Mastercard:9 Schoodic Street").String(); got != "-100.00 CAD" {
		t.Errorf("routed card leg = %s, want -100.00 CAD on the purpose child", got)
	}
	if got := balanceOf(t, log, "Liabilities:PC Mastercard"); got.Units != 0 {
		t.Errorf("registered parent holds %v directly, want none: the leg was routed to the child", got)
	}
}

// The whole point of routing: a purpose-split card still ties to its one bank balance. The connector
// registers on the parent and reconciles against the bank there; each charge's card leg routes to a
// child. A charge that lands after the anchor must move the parent's reconciled books -- which only
// holds if reconcile rolls the subtree up, since the leg itself sits on the child, not the parent.
func TestRoutedChildRollsUpIntoTheParentReconcile(t *testing.T) {
	log := newLog()
	// Anchor the card at zero before any activity, so the opening offset cannot absorb later charges.
	if err := books.AssertBalance(log, "rbc", "Liabilities:PC Mastercard", on(1), cad(0)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}
	importOne(t, log, lineIn("c1", "Liabilities:PC Mastercard", 3, -10000, "KENT SUPPLIES"))
	if err := books.Categorize(log, "human", "Unit 1 reno", "c1", "", "Kent",
		"Liabilities:PC Mastercard:9 Schoodic Street",
		whole("Expenses:Real Estate:Materials:9 Schoodic Street", -10000)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	// The bank now says the whole card owes 100, after the routed charge.
	if err := books.AssertBalance(log, "rbc", "Liabilities:PC Mastercard", on(4), cad(-10000)); err != nil {
		t.Fatalf("AssertBalance: %v", err)
	}

	r := reconcileOne(t, log, "Liabilities:PC Mastercard")
	if !r.Reconciled {
		t.Fatalf("the parent did not roll up the routed child: books=%s bank=%s delta=%s", r.Books, r.Bank, r.Delta)
	}
}

// A parent's reconcile rolls up a routed child's every charge, so a movement the bank saw but the
// books missed still surfaces as the delta rather than hiding on the child. One routed charge lands,
// but the bank shows a second the books never captured: the gap is exactly the missing charge.
func TestParentReconcileStillCatchesAMissedChargeOnAChild(t *testing.T) {
	log := newLog()
	books.AssertBalance(log, "rbc", "Liabilities:PC Mastercard", on(1), cad(0))
	importOne(t, log, lineIn("c1", "Liabilities:PC Mastercard", 3, -10000, "KENT SUPPLIES"))
	books.Categorize(log, "human", "", "c1", "", "Kent", "Liabilities:PC Mastercard:9 Schoodic Street",
		whole("Expenses:Real Estate:Materials:9 Schoodic Street", -10000))
	// The bank says the card owes 130 -- a 30 charge the books never captured.
	books.AssertBalance(log, "rbc", "Liabilities:PC Mastercard", on(4), cad(-13000))

	r := reconcileOne(t, log, "Liabilities:PC Mastercard")
	if r.Reconciled {
		t.Fatal("a missed charge should not reconcile")
	}
	if r.Delta.String() != "-30.00 CAD" {
		t.Errorf("delta = %s, want -30.00 CAD (the missed charge)", r.Delta)
	}
}

// A routing rule self-routes every matching charge, so a purpose-split card needs no per-line
// categorize: register the connector on the parent, add one rule per purpose, and each imported
// charge's card leg lands on its purpose child. The rule and its route survive the log, so the fold
// re-derives the routing on every read.
func TestARoutingRuleSelfRoutesEveryMatchingCharge(t *testing.T) {
	log := newLog()
	if err := books.AddRule(log, "human", rules.Rule{
		Match:    "kent",
		Category: "Expenses:Real Estate:Materials:9 Schoodic Street",
		Source:   "Liabilities:PC Mastercard:9 Schoodic Street",
	}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	importOne(t, log, lineIn("k1", "Liabilities:PC Mastercard", 3, -10000, "KENT SUPPLIES"))
	importOne(t, log, lineIn("k2", "Liabilities:PC Mastercard", 5, -5000, "KENT SUPPLIES #2"))

	if got := entryFor(t, log, "k1").Source; got != "Liabilities:PC Mastercard:9 Schoodic Street" {
		t.Errorf("k1 Source = %q, want the rule's route (survives the log)", got)
	}
	// Both charges self-routed to the purpose child, with no categorize on either line.
	if got := balanceOf(t, log, "Liabilities:PC Mastercard:9 Schoodic Street").String(); got != "-150.00 CAD" {
		t.Errorf("routed child = %s, want -150.00 CAD (both charges routed by the rule)", got)
	}
	if got := balanceOf(t, log, "Liabilities:PC Mastercard"); got.Units != 0 {
		t.Errorf("registered parent holds %v directly, want none: the rule routed both legs to the child", got)
	}
}
