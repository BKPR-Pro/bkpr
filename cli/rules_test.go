package main

import (
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/rules"
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
	r := rules.Rule{Match: "taylor", Category: "Income:Rent:22 Cedar Street", Metadata: map[string]string{"rentapp.lease": "31"}}

	if err := upsertRule(log, r, map[string]bool{"category": true, "meta": true}, "", "", "human"); err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "taylor")
	if got.Category != "Income:Rent:22 Cedar Street" || got.Metadata["rentapp.lease"] != "31" {
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

// A tax's scope and bound are authored like the rest of the tax: they land on the rule, and on a
// change only the named fields move, so scoping an existing tax leaves its rate and account alone.
func TestUpsertRuleSetsTheTaxScope(t *testing.T) {
	log := ruleLog(t)
	seed := rules.Rule{Match: "kent", Category: "Expenses:Real Estate:Materials:9 Birch Street", TaxRate: "15%", TaxAccount: "Assets:HST ITC"}
	if err := books.AddRule(log, "human", seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	change := rules.Rule{Match: "kent", TaxCategory: `9 birch street`, TaxFrom: "2026-01-01"}
	err := upsertRule(log, change, map[string]bool{"tax-category": true, "tax-from": true}, "", "scope the ITC", "human")
	if err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "kent")
	if got.TaxCategory != `9 birch street` || got.TaxFrom != "2026-01-01" {
		t.Errorf("scoped rule = %+v, want the scope and from-date set", got)
	}
	if got.TaxRate != "15%" || got.TaxAccount != "Assets:HST ITC" {
		t.Errorf("rule = %+v, want the untouched tax preserved", got)
	}
}

// A scope without a tax has nothing to scope, so it is refused at authoring like a rate without an
// account, before a poison rule can reach the log.
func TestUpsertRuleRejectsATaxScopeWithoutATax(t *testing.T) {
	log := ruleLog(t)
	r := rules.Rule{Match: "kent", Category: "Expenses:Materials", TaxCategory: `9 birch street`}

	if err := upsertRule(log, r, map[string]bool{"category": true, "tax-category": true}, "", "", "human"); err == nil {
		t.Fatal("expected an error for a tax scope with no tax")
	}
}

// -tax-from takes the same written forms every other date flag does and lands on the rule in the
// one form the engine reads, so "Feb 1, 2026" and 2026-02-01 author the same rule.
func TestTaxFromDateNormalizes(t *testing.T) {
	got, err := taxFromDate("Feb 1, 2026")
	if err != nil {
		t.Fatalf("taxFromDate: %v", err)
	}
	if got != "2026-02-01" {
		t.Errorf("normalized = %q, want 2026-02-01", got)
	}
	if _, err := taxFromDate("not a date"); err == nil {
		t.Fatal("expected an error for an unreadable date")
	}
}

// Metadata merges per key on a change, so naming one key does not drop the others.
func TestUpsertRuleMergesMetadata(t *testing.T) {
	log := ruleLog(t)
	seed := rules.Rule{Match: "taylor", Metadata: map[string]string{"rentapp.lease": "31", "channel": "etransfer"}}
	if err := books.AddRule(log, "human", seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := upsertRule(log, rules.Rule{Match: "taylor", Metadata: map[string]string{"rentapp.lease": "47"}}, map[string]bool{"meta": true}, "", "new lease", "human")
	if err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	got := find(t, log, "taylor")
	if got.Metadata["rentapp.lease"] != "47" {
		t.Errorf("rentapp.lease = %q, want the update 47", got.Metadata["rentapp.lease"])
	}
	if got.Metadata["channel"] != "etransfer" {
		t.Errorf("channel = %q, want the untouched key preserved", got.Metadata["channel"])
	}
}
