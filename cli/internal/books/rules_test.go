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

func loaded(t *testing.T, log *eventlog.Log, want ...rules.Rule) books.LoadResult {
	t.Helper()
	got, err := books.LoadRules(log, "human", "", want)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	return got
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

func TestLoadingIntoAnEmptyLogAddsEveryRuleInOrder(t *testing.T) {
	log := newLog()

	got := loaded(t, log, rule("city water", "Expenses:Water"), rule("water", "Expenses:Wrong"))

	if got.Added != 2 || got.Changed != 0 || got.Removed != 0 || got.Moved != 0 {
		t.Errorf("got %+v, want 2 added and nothing else", got)
	}
	assertOrder(t, log, "city water", "water")
}

// The file is an editing surface, not a source of truth. Loading it unchanged must say nothing,
// or every run would append noise to a log that is supposed to record intent.
func TestLoadingTheSameFileTwiceRecordsNothingTheSecondTime(t *testing.T) {
	log := newLog()
	set := []rules.Rule{rule("city water", "Expenses:Water"), rule("acme", "Expenses:Materials")}

	loaded(t, log, set...)
	got := loaded(t, log, set...)

	if got != (books.LoadResult{}) {
		t.Errorf("got %+v, want nothing recorded", got)
	}
}

// Changing where a merchant posts is the most consequential edit in the system, because it
// reclassifies every past line the rule matched. It is recorded as one fact, with a reason.
func TestChangingARulesAnswerIsRecordedWithItsReason(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Real Estate:Materials:Uncategorized"))

	got, err := books.LoadRules(log, "human", "the receipts were all Unit 1",
		[]rules.Rule{rule("acme", "Expenses:Real Estate:Materials:Unit 1")})
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if got.Changed != 1 || got.Added != 0 {
		t.Fatalf("got %+v, want 1 changed", got)
	}

	rs, _ := books.Rules(log)
	if rs[0].Category != "Expenses:Real Estate:Materials:Unit 1" {
		t.Errorf("category = %q, want the new one", rs[0].Category)
	}

	events, _ := log.All()
	last := events[len(events)-1]
	if last.Action != "changed" {
		t.Fatalf("last action = %q", last.Action)
	}
	if !strings.Contains(string(last.Data), "the receipts were all Unit 1") {
		t.Errorf("the reason was not recorded: %s", last.Data)
	}
}

func TestDroppingARuleFromTheFileRemovesIt(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials"), rule("city water", "Expenses:Water"))

	got := loaded(t, log, rule("city water", "Expenses:Water"))

	if got.Removed != 1 {
		t.Errorf("got %+v, want 1 removed", got)
	}
	assertOrder(t, log, "city water")
}

// Order is semantic: `city water` must beat `water`. A rule inserted at the top of the file lands
// at the top of the set, not at the end of it.
func TestInsertingARuleAtTheTopKeepsTheFilesOrder(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("water", "Expenses:Wrong"), rule("acme", "Expenses:Materials"))

	got := loaded(t, log,
		rule("city water", "Expenses:Water"),
		rule("water", "Expenses:Wrong"),
		rule("acme", "Expenses:Materials"),
	)

	if got.Added != 1 || got.Moved != 0 {
		t.Errorf("got %+v, want 1 added and no moves", got)
	}
	assertOrder(t, log, "city water", "water", "acme")
}

func TestReorderingExistingRulesIsRecordedAsMoves(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("water", "Expenses:Wrong"), rule("city water", "Expenses:Water"))

	got := loaded(t, log, rule("city water", "Expenses:Water"), rule("water", "Expenses:Wrong"))

	if got.Moved != 1 || got.Added != 0 || got.Changed != 0 {
		t.Errorf("got %+v, want 1 moved", got)
	}
	assertOrder(t, log, "city water", "water")
}

func TestAFullReversalLandsInTheFilesOrder(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("a", "Expenses:A"), rule("b", "Expenses:B"), rule("c", "Expenses:C"))

	loaded(t, log, rule("c", "Expenses:C"), rule("b", "Expenses:B"), rule("a", "Expenses:A"))

	assertOrder(t, log, "c", "b", "a")
}

// Adds, changes, removes and moves in one edit, which is what a real rules file edit looks like.
func TestEveryKindOfEditInOneLoad(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("a", "Expenses:A"), rule("b", "Expenses:B"), rule("gone", "Expenses:X"))

	got := loaded(t, log,
		rule("b", "Expenses:B"),
		rule("new", "Expenses:New"),
		rule("a", "Expenses:Changed"),
	)

	if got.Added != 1 || got.Changed != 1 || got.Removed != 1 || got.Moved != 1 {
		t.Errorf("got %+v, want one of each", got)
	}
	assertOrder(t, log, "b", "new", "a")
}

// A rule removed and later restored is two separate lives, not a resurrection of the first.
func TestARuleCanBeRemovedAndAddedAgain(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials"))
	loaded(t, log)
	assertOrder(t, log)

	got := loaded(t, log, rule("acme", "Expenses:Repairs"))

	if got.Added != 1 {
		t.Errorf("got %+v, want 1 added", got)
	}
	rs, _ := books.Rules(log)
	if len(rs) != 1 || rs[0].Category != "Expenses:Repairs" {
		t.Errorf("got %+v, want the rule back with its new answer", rs)
	}
}

// The match pattern is the rule's identity. Two rules cannot share one.
func TestTwoRulesWithTheSameMatchAreRefused(t *testing.T) {
	log := newLog()

	if _, err := books.LoadRules(log, "human", "", []rules.Rule{
		rule("acme", "Expenses:Materials"),
		rule("acme", "Expenses:Repairs"),
	}); err == nil {
		t.Fatal("loaded two rules with the same match")
	}

	assertOrder(t, log)
}

func TestARuleWithNoMatchIsRefused(t *testing.T) {
	log := newLog()

	if _, err := books.LoadRules(log, "human", "", []rules.Rule{{Category: "Expenses:X"}}); err == nil {
		t.Fatal("loaded a rule with no match")
	}
}
