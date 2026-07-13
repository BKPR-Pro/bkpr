package rules_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
)

func txAmt(description string, cents int64) model.Transaction {
	return model.Transaction{
		Description: description, Account: "Assets:Bank:Chequing",
		Amount: model.Amount{Units: cents, Scale: 2, Commodity: "CAD"},
	}
}

func cad(cents int64) *model.Amount {
	return &model.Amount{Units: cents, Scale: 2, Commodity: "CAD"}
}

// FR-3: two payees share one memo but split by amount. An amount predicate narrows a rule to lines
// of a given magnitude, so the $175 property tax and the $155 one become two rules on one pattern.
func TestAnAmountPredicateNarrowsARuleToLinesOfThatMagnitude(t *testing.T) {
	lisgar := rules.Rule{Match: `PROV NB PROP TX`, Amount: cad(17500), Category: "Expenses:Property:22 Lisgar"}
	schoodic := rules.Rule{Match: `PROV NB PROP TX`, Amount: cad(15500), Category: "Expenses:Property:9 Schoodic"}
	e := engine(t, lisgar, schoodic)

	if got := only(t, e.Apply(txAmt("PROV NB PROP TX", -17500))).Account; got != "Expenses:Property:22 Lisgar" {
		t.Errorf("$175 line -> %q, want 22 Lisgar", got)
	}
	if got := only(t, e.Apply(txAmt("PROV NB PROP TX", -15500))).Account; got != "Expenses:Property:9 Schoodic" {
		t.Errorf("$155 line -> %q, want 9 Schoodic", got)
	}
}

// The predicate matches the line's magnitude, so the statement's debit/credit sign is not part of
// it: a bill paid (a debit) and the same amount refunded (a credit) both match.
func TestTheAmountPredicateMatchesMagnitudeNotSign(t *testing.T) {
	e := engine(t, rules.Rule{Match: `PROV NB PROP TX`, Amount: cad(17500), Category: "Expenses:Property:22 Lisgar"})
	if e.Apply(txAmt("PROV NB PROP TX", 17500)).Uncategorized() {
		t.Error("a +175 line should match the 175 predicate too")
	}
}

// A line whose magnitude does not match an amount-qualified rule falls through it -- to a broader
// rule on the same pattern, or to Uncategorized.
func TestAnAmountQualifiedRuleDoesNotFireOnOtherAmounts(t *testing.T) {
	e := engine(t, rules.Rule{Match: `PROV NB PROP TX`, Amount: cad(17500), Category: "Expenses:Property:22 Lisgar"})
	if !e.Apply(txAmt("PROV NB PROP TX", -31000)).Uncategorized() {
		t.Error("a $310 line should not match the $175 rule")
	}
}

// An amount-qualified rule beats a broader amountless one on the same pattern when both match, and
// the amountless rule catches every other amount: the specific split rides in front of the default.
func TestAmountRuleAndAmountlessFallbackCoexist(t *testing.T) {
	specific := rules.Rule{Match: `ST.STEPHEN UTL`, Amount: cad(5000), Category: "Expenses:Utilities:22 Lisgar"}
	fallback := rules.Rule{Match: `ST.STEPHEN UTL`, Category: "Expenses:Utilities:Uncategorized"}
	e := engine(t, specific, fallback)

	if got := only(t, e.Apply(txAmt("ST.STEPHEN UTL", -5000))).Account; got != "Expenses:Utilities:22 Lisgar" {
		t.Errorf("$50 line -> %q, want the specific 22 Lisgar rule", got)
	}
	if got := only(t, e.Apply(txAmt("ST.STEPHEN UTL", -15000))).Account; got != "Expenses:Utilities:Uncategorized" {
		t.Errorf("$150 line -> %q, want the amountless fallback", got)
	}
}
