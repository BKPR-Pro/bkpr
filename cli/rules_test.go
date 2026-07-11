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

	if err := upsertRule(log, r, map[string]bool{"category": true, "meta": true}, "", ""); err != nil {
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

	err := upsertRule(log, rules.Rule{Match: "acme", Category: "Expenses:New"}, map[string]bool{"category": true}, "", "reclassify")
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

// Metadata merges per key on a change, so naming one key does not drop the others.
func TestUpsertRuleMergesMetadata(t *testing.T) {
	log := ruleLog(t)
	seed := rules.Rule{Match: "hyungjin", Metadata: map[string]string{"rentapp.lease": "31", "channel": "etransfer"}}
	if err := books.AddRule(log, "human", seed, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := upsertRule(log, rules.Rule{Match: "hyungjin", Metadata: map[string]string{"rentapp.lease": "47"}}, map[string]bool{"meta": true}, "", "new lease")
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
