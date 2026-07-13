package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/rules"
)

func rule(match, category string) rules.Rule {
	return rules.Rule{Match: match, Category: category}
}

func matches(rs []rules.Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Match
	}
	return out
}

// loaded appends rules in order, the common setup the other suites rely on.
func loaded(t *testing.T, log *eventlog.Log, want ...rules.Rule) {
	t.Helper()
	for _, r := range want {
		if err := books.AddRule(log, "human", r, ""); err != nil {
			t.Fatalf("AddRule(%q): %v", r.Match, err)
		}
	}
}

func assertOrder(t *testing.T, log *eventlog.Log, want ...string) {
	t.Helper()
	rs, err := books.Rules(log)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if got := strings.Join(matches(rs), ","); got != strings.Join(want, ",") {
		t.Fatalf("order = [%s], want [%s]", got, strings.Join(want, ","))
	}
}

func TestAddRuleAppendsInOrder(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("city water", "Expenses:Water"), rule("water", "Expenses:Wrong"))
	assertOrder(t, log, "city water", "water")
}

// A rule's metadata is part of the rule, so it must survive the round trip through the log: the
// event stores it and the fold rebuilds it. This is what lets a destination read rentapp.lease off
// a rule that was registered in a past session.
func TestARulesMetadataSurvivesTheLog(t *testing.T) {
	log := newLog()
	r := rule("hyungjin", "Income:Real Estate:Rent:22 Lisgar Street")
	r.Metadata = map[string]string{"rentapp.lease": "31"}
	loaded(t, log, r)

	set, err := books.Rules(log)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(set) != 1 || set[0].Metadata["rentapp.lease"] != "31" {
		t.Errorf("folded rule = %+v, want rentapp.lease=31 intact", set[0])
	}
}

// A rule's tax rate and account are part of the rule, so they must survive the round trip: the
// event stores them and the fold rebuilds them, which is what makes the split retroactive — set a
// vendor's rate today and every past line it matched re-splits on the next read.
func TestARulesTaxSurvivesTheLog(t *testing.T) {
	log := newLog()
	r := rule("acme", "Expenses:Repairs:Materials")
	r.TaxRate, r.TaxAccount = "15%", "Assets:HST ITC"
	loaded(t, log, r)

	set, err := books.Rules(log)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(set) != 1 || set[0].TaxRate != "15%" || set[0].TaxAccount != "Assets:HST ITC" {
		t.Errorf("folded rule = %+v, want 15%% to Assets:HST ITC intact", set[0])
	}
}

// End to end: a taxed vendor's imported line splits in the rendered books — the pre-tax amount on
// the category, the tax extracted from the same total on its own account — and the two still
// balance the statement line. Nothing about the split is stored; it re-derives from the rule.
func TestLedgerSplitsTaxOnATaxedVendor(t *testing.T) {
	log := newLog()
	r := rule("acme", "Expenses:Repairs:Materials")
	r.TaxRate, r.TaxAccount = "15%", "Assets:HST ITC"
	loaded(t, log, r)
	importOne(t, log, line("acme", 3, -11500, "ACME HARDWARE #4471")) // 115.00 out, tax included

	txs, entries := ledger(t, log)
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}
	if !entries[0].Balances(txs[0]) {
		t.Fatalf("split entry does not balance: %+v", entries[0].Postings)
	}
	got := map[string]string{}
	for _, p := range entries[0].Postings {
		got[p.Account] = p.Amount.String()
	}
	if got["Expenses:Repairs:Materials"] != "100.00 CAD" {
		t.Errorf("net posting = %q, want 100.00 CAD", got["Expenses:Repairs:Materials"])
	}
	if got["Assets:HST ITC"] != "15.00 CAD" {
		t.Errorf("tax posting = %q, want 15.00 CAD", got["Assets:HST ITC"])
	}
}

// End to end: a rule's metadata rides the fold onto the entry the export reads, so a categorized rent
// deposit names the lease it should be recorded against.
func TestLedgerCarriesRuleMetadataOntoTheEntry(t *testing.T) {
	log := newLog()
	r := rule("hyungjin", "Income:Real Estate:Rent:22 Lisgar Street")
	r.Metadata = map[string]string{"rentapp.lease": "31"}
	loaded(t, log, r)
	importOne(t, log, line("dep", 3, 168000, "E-TRANSFER FROM HYUNGJIN SON"))

	_, entries := ledger(t, log)
	if len(entries) != 1 || entries[0].Metadata["rentapp.lease"] != "31" {
		t.Fatalf("entry metadata = %v, want rentapp.lease=31", entries[0].Metadata)
	}
}

// Order is semantic, so a rule can be placed ahead of another: `city water` must beat `water`.
func TestAddRuleBeforePositionsIt(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("water", "Expenses:Wrong"), rule("acme", "Expenses:Materials"))

	if err := books.AddRule(log, "human", rule("city water", "Expenses:Water"), "water"); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	assertOrder(t, log, "city water", "water", "acme")
}

// The match pattern is a rule's identity, so it cannot be added twice.
func TestAddingADuplicateMatchIsRefused(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials"))

	if err := books.AddRule(log, "human", rule("acme", "Expenses:Repairs"), ""); err == nil {
		t.Fatal("added a second rule with the same match")
	}
}

func TestAddRuleWithNoMatchIsRefused(t *testing.T) {
	if err := books.AddRule(newLog(), "human", rules.Rule{Category: "Expenses:X"}, ""); err == nil {
		t.Fatal("added a rule with no match")
	}
}

// Changing a rule reclassifies every past line it matched, so it carries a reason.
func TestSetRuleChangesTheAnswerAndRecordsWhy(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Real Estate:Materials:Uncategorized"))

	err := books.SetRule(log, "human", "the receipts were all Unit 1",
		rule("acme", "Expenses:Real Estate:Materials:Unit 1"))
	if err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	rs, _ := books.Rules(log)
	if rs[0].Category != "Expenses:Real Estate:Materials:Unit 1" {
		t.Errorf("category = %q", rs[0].Category)
	}
	events, _ := log.All()
	if last := events[len(events)-1]; last.Action != "changed" || !strings.Contains(string(last.Data), "the receipts were all Unit 1") {
		t.Errorf("expected a changed event carrying the reason: %s %s", last.Action, last.Data)
	}
}

func TestSetRuleOnAMissingRuleIsRefused(t *testing.T) {
	if err := books.SetRule(newLog(), "human", "", rule("ghost", "Expenses:X")); err == nil {
		t.Fatal("changed a rule that does not exist")
	}
}

func TestRemoveRuleDropsIt(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials"), rule("city water", "Expenses:Water"))

	if err := books.RemoveRule(log, "human", "acme", nil); err != nil {
		t.Fatalf("RemoveRule: %v", err)
	}
	assertOrder(t, log, "city water")
}

func TestRemovingAMissingRuleIsRefused(t *testing.T) {
	if err := books.RemoveRule(newLog(), "human", "ghost", nil); err == nil {
		t.Fatal("removed a rule that does not exist")
	}
}

func TestMoveRuleReorders(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("water", "Expenses:Wrong"), rule("city water", "Expenses:Water"))

	if err := books.MoveRule(log, "human", "city water", nil, "water"); err != nil {
		t.Fatalf("MoveRule: %v", err)
	}
	assertOrder(t, log, "city water", "water")
}

func TestMoveRuleToTheEnd(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("a", "Expenses:A"), rule("b", "Expenses:B"), rule("c", "Expenses:C"))

	if err := books.MoveRule(log, "human", "a", nil, ""); err != nil {
		t.Fatalf("MoveRule: %v", err)
	}
	assertOrder(t, log, "b", "c", "a")
}

// A rule removed and later added again is two separate lives, not a resurrection of the first.
func TestARuleCanBeRemovedAndAddedAgain(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials"))
	if err := books.RemoveRule(log, "human", "acme", nil); err != nil {
		t.Fatal(err)
	}
	loaded(t, log, rule("acme", "Expenses:Repairs"))

	if rs, _ := books.Rules(log); len(rs) != 1 || rs[0].Category != "Expenses:Repairs" {
		t.Errorf("got %+v, want the rule back with its new answer", rs)
	}
}
