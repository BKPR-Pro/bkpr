package main

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/rules"
)

func ruleLog(t *testing.T) *eventlog.Log {
	t.Helper()
	return eventlog.New(eventlog.NewMemory())
}

func find(t *testing.T, log *eventlog.Log, match string) rules.Rule {
	t.Helper()
	set, err := books.Rules(log)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	for _, r := range set {
		if r.Match == match {
			return r
		}
	}
	t.Fatalf("no rule %q in %+v", match, set)
	return rules.Rule{}
}

// set is one verb: a pattern not yet known is created.
func TestUpsertRuleCreatesWhenAbsent(t *testing.T) {
	log := ruleLog(t)
	r := rules.Rule{Match: "hyungjin", Category: "Income:Rent:22 Lisgar", Metadata: map[string]string{"rentapp.lease": "31"}}

	if err := upsertRule(log, r, map[string]bool{"category": true, "meta": true}, "", "", "human"); err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "hyungjin")
	if got.Category != "Income:Rent:22 Lisgar" || got.Metadata["rentapp.lease"] != "31" {
		t.Errorf("created rule = %+v", got)
	}
}

// A known pattern is changed, and only the named fields move: setting a category leaves the payee
// as it was rather than blanking it.
func TestUpsertRuleChangesOnlyNamedFields(t *testing.T) {
	log := ruleLog(t)
	if err := books.AddRule(log, "human", rules.Rule{Match: "acme", Payee: "Acme Co", Category: "Expenses:Old"}, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := upsertRule(log, rules.Rule{Match: "acme", Category: "Expenses:New"}, map[string]bool{"category": true}, "", "reclassify", "human")
	if err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "acme")
	if got.Category != "Expenses:New" {
		t.Errorf("category = %q, want the change", got.Category)
	}
	if got.Payee != "Acme Co" {
		t.Errorf("payee = %q, want it preserved", got.Payee)
	}
}

// A vendor known to be taxed is authored with a rate and the account the tax posts to, and both
// survive onto the rule so every line it matches splits the tax out of the total.
func TestUpsertRuleSetsTax(t *testing.T) {
	log := ruleLog(t)
	r := rules.Rule{Match: "acme", Category: "Expenses:Repairs:Materials", TaxRate: "15%", TaxAccount: "Assets:HST ITC"}

	err := upsertRule(log, r, map[string]bool{"category": true, "tax-rate": true, "tax-account": true}, "", "", "human")
	if err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "acme")
	if got.TaxRate != "15%" || got.TaxAccount != "Assets:HST ITC" {
		t.Errorf("taxed rule = %+v, want 15%% to Assets:HST ITC", got)
	}
}

// A rate with nowhere to post the tax is refused at authoring, so a poison rule never reaches the
// log to break every later read of the books.
func TestUpsertRuleRejectsTaxRateWithoutAccount(t *testing.T) {
	log := ruleLog(t)
	r := rules.Rule{Match: "acme", Category: "Expenses:Repairs", TaxRate: "15%"}

	if err := upsertRule(log, r, map[string]bool{"category": true, "tax-rate": true}, "", "", "human"); err == nil {
		t.Fatal("expected an error for a tax rate with no account")
	}
}

// Metadata merges per key on a change, so naming one key does not drop the others.
func TestUpsertRuleMergesMetadata(t *testing.T) {
	log := ruleLog(t)
	seed := rules.Rule{Match: "hyungjin", Metadata: map[string]string{"rentapp.lease": "31", "channel": "etransfer"}}
	if err := books.AddRule(log, "human", seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := upsertRule(log, rules.Rule{Match: "hyungjin", Metadata: map[string]string{"rentapp.lease": "47"}}, map[string]bool{"meta": true}, "", "new lease", "human")
	if err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "hyungjin")
	if got.Metadata["rentapp.lease"] != "47" {
		t.Errorf("rentapp.lease = %q, want the update 47", got.Metadata["rentapp.lease"])
	}
	if got.Metadata["channel"] != "etransfer" {
		t.Errorf("channel = %q, want the untouched key preserved", got.Metadata["channel"])
	}
}
