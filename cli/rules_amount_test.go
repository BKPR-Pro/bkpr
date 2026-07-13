package main

import (
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

func importAmt(t *testing.T, id, desc string, cents int64) {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = books.Import(s.Log, "statement:test", []model.Transaction{{
		ID: id, Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: cents, Scale: 2, Commodity: "CAD"}, Description: desc,
	}})
	s.Close()
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
}

func categoriesByTx(t *testing.T) map[string]string {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	txs, entries, err := books.Ledger(s.Log)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	out := map[string]string{}
	for i, tx := range txs {
		if len(entries[i].Postings) > 0 {
			out[tx.ID] = entries[i].Postings[0].Account
		}
	}
	return out
}

// FR-3 end to end: two property-tax lines share a memo and split only by amount. An amount-qualified
// rule each routes them to different accounts in one categorization pass, no per-line work.
func TestRulesSetAmountRoutesLinesByAmount(t *testing.T) {
	bookHere(t)
	importAmt(t, "lisgar", "PROV NB PROP TX", -17500)
	importAmt(t, "schoodic", "PROV NB PROP TX", -15500)

	if err := ruleSet([]string{"set", "PROV NB PROP TX", "-amount", "175", "-category", "Expenses:Property:22 Lisgar"}); err != nil {
		t.Fatalf("rules set 175: %v", err)
	}
	if err := ruleSet([]string{"set", "PROV NB PROP TX", "-amount", "155", "-category", "Expenses:Property:9 Schoodic"}); err != nil {
		t.Fatalf("rules set 155: %v", err)
	}

	got := categoriesByTx(t)
	if got["lisgar"] != "Expenses:Property:22 Lisgar" {
		t.Errorf("$175 line -> %q, want 22 Lisgar", got["lisgar"])
	}
	if got["schoodic"] != "Expenses:Property:9 Schoodic" {
		t.Errorf("$155 line -> %q, want 9 Schoodic", got["schoodic"])
	}
}

// Setting the same pattern and amount twice edits in place rather than colliding, so a category can
// be corrected without removing the rule first.
func TestRulesSetAmountEditsInPlace(t *testing.T) {
	bookHere(t)
	importAmt(t, "lisgar", "PROV NB PROP TX", -17500)

	if err := ruleSet([]string{"set", "PROV NB PROP TX", "-amount", "175", "-category", "Wrong"}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := ruleSet([]string{"set", "PROV NB PROP TX", "-amount", "175", "-category", "Expenses:Property:22 Lisgar"}); err != nil {
		t.Fatalf("second set: %v", err)
	}
	if got := categoriesByTx(t)["lisgar"]; got != "Expenses:Property:22 Lisgar" {
		t.Errorf("line -> %q, want the corrected 22 Lisgar", got)
	}
}

// rm names the variant by its amount, so removing the $175 rule leaves the $155 one on the pattern.
func TestRulesRmAmountRemovesOneVariant(t *testing.T) {
	bookHere(t)
	if err := ruleSet([]string{"set", "X", "-amount", "175", "-category", "A"}); err != nil {
		t.Fatalf("set 175: %v", err)
	}
	if err := ruleSet([]string{"set", "X", "-amount", "155", "-category", "B"}); err != nil {
		t.Fatalf("set 155: %v", err)
	}
	if err := ruleSet([]string{"rm", "X", "-amount", "175"}); err != nil {
		t.Fatalf("rm 175: %v", err)
	}

	s, _ := store.Open(".")
	defer s.Close()
	rs, _ := books.Rules(s.Log)
	if len(rs) != 1 || rs[0].Category != "B" {
		t.Fatalf("want only the $155 B rule left, got %+v", rs)
	}
}
