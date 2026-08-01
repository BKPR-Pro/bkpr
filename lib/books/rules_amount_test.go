package books_test

import (
	"testing"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/rules"
)

func ruleAmt(match, category string, cents int64) rules.Rule {
	a := model.Amount{Units: cents, Scale: 2, Commodity: "CAD"}
	return rules.Rule{Match: match, Category: category, Amount: &a}
}

// FR-3: a rule's identity is its pattern together with its amount predicate, so two rules on one
// pattern that differ only by amount coexist through the fold, each keeping its own amount.
func TestTwoRulesShareAPatternButSplitByAmount(t *testing.T) {
	log := newLog()
	loaded(t, log,
		ruleAmt("PROV NB PROP TX", "Expenses:Property:22 Lisgar", 17500),
		ruleAmt("PROV NB PROP TX", "Expenses:Property:9 Schoodic", 15500),
	)

	rs, err := books.Rules(log)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(rs) != 2 {
		t.Fatalf("want 2 coexisting rules on one pattern, got %d", len(rs))
	}
	if rs[0].Amount == nil || rs[0].Amount.String() != "175.00 CAD" {
		t.Errorf("first rule amount = %v, want 175.00 CAD", rs[0].Amount)
	}
	if rs[1].Amount == nil || rs[1].Amount.String() != "155.00 CAD" {
		t.Errorf("second rule amount = %v, want 155.00 CAD", rs[1].Amount)
	}
}

// Same pattern and same amount is the same rule, so a second add is a duplicate, exactly as a
// repeated amountless pattern is.
func TestSamePatternSameAmountIsADuplicate(t *testing.T) {
	log := newLog()
	loaded(t, log, ruleAmt("X", "A", 10000))
	if err := books.AddRule(log, "human", ruleAmt("X", "B", 10000), ""); err == nil {
		t.Fatal("same pattern and amount should be a duplicate")
	}
}

// Same pattern, different amount is a different rule, so it adds rather than colliding.
func TestSamePatternDifferentAmountIsNotADuplicate(t *testing.T) {
	log := newLog()
	loaded(t, log, ruleAmt("X", "A", 10000))
	if err := books.AddRule(log, "human", ruleAmt("X", "B", 20000), ""); err != nil {
		t.Fatalf("a different amount on the same pattern should be allowed: %v", err)
	}
	if rs, _ := books.Rules(log); len(rs) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rs))
	}
}

// Changing a rule targets it by full identity, so setting the $200 rule leaves the $100 one on the
// same pattern untouched.
func TestSetRuleTargetsByAmountIdentity(t *testing.T) {
	log := newLog()
	loaded(t, log, ruleAmt("X", "A", 10000), ruleAmt("X", "B", 20000))

	if err := books.SetRule(log, "human", "fix", ruleAmt("X", "B2", 20000)); err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	rs, _ := books.Rules(log)
	for _, r := range rs {
		switch r.Amount.String() {
		case "100.00 CAD":
			if r.Category != "A" {
				t.Errorf("the $100 rule changed to %q; it should be untouched", r.Category)
			}
		case "200.00 CAD":
			if r.Category != "B2" {
				t.Errorf("the $200 rule = %q, want B2", r.Category)
			}
		}
	}
}

// Removing a rule targets it by full identity, so removing the $200 rule leaves the $100 one.
func TestRemoveRuleTargetsByAmountIdentity(t *testing.T) {
	log := newLog()
	loaded(t, log, ruleAmt("X", "A", 10000), ruleAmt("X", "B", 20000))
	two := model.Amount{Units: 20000, Scale: 2, Commodity: "CAD"}

	if err := books.RemoveRule(log, "human", "X", &two); err != nil {
		t.Fatalf("RemoveRule: %v", err)
	}

	rs, _ := books.Rules(log)
	if len(rs) != 1 || rs[0].Category != "A" {
		t.Fatalf("want only the $100 A rule left, got %+v", matches(rs))
	}
}

// Identity is canonical in sign and trailing zeros, so a rule set at "175.00" is the same rule an
// rm names as "175" -- the reference does not have to echo the exact decimals.
func TestAmountIdentityIsCanonical(t *testing.T) {
	log := newLog()
	loaded(t, log, ruleAmt("X", "A", 17500)) // 175.00
	whole := model.Amount{Units: 175, Scale: 0, Commodity: "CAD"}

	if err := books.RemoveRule(log, "human", "X", &whole); err != nil {
		t.Fatalf("RemoveRule with 175 should hit the 175.00 rule: %v", err)
	}
	if rs, _ := books.Rules(log); len(rs) != 0 {
		t.Fatalf("the rule should be gone, got %d", len(rs))
	}
}
