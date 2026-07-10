package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/books"
	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
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

	if err := books.RemoveRule(log, "human", "acme"); err != nil {
		t.Fatalf("RemoveRule: %v", err)
	}
	assertOrder(t, log, "city water")
}

func TestRemovingAMissingRuleIsRefused(t *testing.T) {
	if err := books.RemoveRule(newLog(), "human", "ghost"); err == nil {
		t.Fatal("removed a rule that does not exist")
	}
}

func TestMoveRuleReorders(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("water", "Expenses:Wrong"), rule("city water", "Expenses:Water"))

	if err := books.MoveRule(log, "human", "city water", "water"); err != nil {
		t.Fatalf("MoveRule: %v", err)
	}
	assertOrder(t, log, "city water", "water")
}

func TestMoveRuleToTheEnd(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("a", "Expenses:A"), rule("b", "Expenses:B"), rule("c", "Expenses:C"))

	if err := books.MoveRule(log, "human", "a", ""); err != nil {
		t.Fatalf("MoveRule: %v", err)
	}
	assertOrder(t, log, "b", "c", "a")
}

// A rule removed and later added again is two separate lives, not a resurrection of the first.
func TestARuleCanBeRemovedAndAddedAgain(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials"))
	if err := books.RemoveRule(log, "human", "acme"); err != nil {
		t.Fatal(err)
	}
	loaded(t, log, rule("acme", "Expenses:Repairs"))

	if rs, _ := books.Rules(log); len(rs) != 1 || rs[0].Category != "Expenses:Repairs" {
		t.Errorf("got %+v, want the rule back with its new answer", rs)
	}
}
