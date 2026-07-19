package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dallasread/bkpr/lib/model"
	"github.com/dallasread/bkpr/lib/rules"
)

// A rule's amount predicate is part of its identity, so rules list shows it -- otherwise two rules
// on one pattern that split by amount would read as indistinguishable rows.
func TestRenderRulesShowsTheAmountPredicate(t *testing.T) {
	a := model.Amount{Units: 17500, Scale: 2, Commodity: "CAD"}
	set := []rules.Rule{
		{Match: "PROV NB PROP TX", Category: "Expenses:Property:22 Lisgar", Amount: &a},
		{Match: "acme", Category: "Expenses:Repairs"},
	}

	var buf bytes.Buffer
	renderRules(&buf, set)
	out := buf.String()

	if !strings.Contains(out, "AMOUNT") {
		t.Error("the header should carry an AMOUNT column")
	}
	if !strings.Contains(out, "175.00 CAD") {
		t.Errorf("an amount-qualified rule should show its amount, got:\n%s", out)
	}
}
