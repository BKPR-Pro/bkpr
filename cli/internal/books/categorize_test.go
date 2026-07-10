package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/cli/internal/books"
	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
	"github.com/dallasread/bookkeeper/cli/internal/model"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
)

func whole(account string, cents int64) []model.Posting {
	return []model.Posting{{Account: account, Amount: cad(-cents)}}
}

func ledger(t *testing.T, log *eventlog.Log) ([]model.Transaction, []model.Entry) {
	t.Helper()
	txs, entries, err := books.Ledger(log)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	return txs, entries
}

func entryFor(t *testing.T, log *eventlog.Log, id string) model.Entry {
	t.Helper()
	txs, entries := ledger(t, log)
	for i, tx := range txs {
		if tx.ID == id {
			return entries[i]
		}
	}
	t.Fatalf("no entry for %q", id)
	return model.Entry{}
}

func importOne(t *testing.T, log *eventlog.Log, tx model.Transaction) {
	t.Helper()
	if _, err := books.Import(log, "statement:test", []model.Transaction{tx}); err != nil {
		t.Fatalf("Import: %v", err)
	}
}

// Without an assertion, a line is categorized by the rules, and a line no rule matches falls to
// Uncategorized. This is the fold Ledger does before any override.
func TestLedgerAppliesTheRulesWhenNothingIsAsserted(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Repairs"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	if got := entryFor(t, log, "a").Postings[0].Account; got != "Expenses:Repairs" {
		t.Errorf("account = %q, want the rule's", got)
	}
}

// A person or a model asserts the postings for one line, and that assertion wins over the rule.
// This is the first time the human tier is real rather than designed.
func TestAnAssertionOverridesTheRuleForThatLine(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Real Estate:Materials:Uncategorized"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	err := books.Categorize(log, "human", "the receipt was Unit 1", "a", "Acme Hardware",
		whole("Expenses:Real Estate:Materials:45 Sample Avenue:Unit 1", -8420))
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	got := entryFor(t, log, "a")
	if got.Postings[0].Account != "Expenses:Real Estate:Materials:45 Sample Avenue:Unit 1" {
		t.Errorf("account = %q, want the asserted one", got.Postings[0].Account)
	}
	if got.Payee != "Acme Hardware" {
		t.Errorf("payee = %q", got.Payee)
	}
}

// The override is a fact about one transaction, keyed by its fingerprint. It must not touch any
// other line, which is the whole reason a correction never generalizes into a rule.
func TestAnAssertionTouchesOnlyItsOwnLine(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials:Uncategorized"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	importOne(t, log, line("b", 9, -4000, "ACME HARDWARE"))

	if err := books.Categorize(log, "human", "", "a", "Acme", whole("Expenses:Materials:Unit 1", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "b").Postings[0].Account; got != "Expenses:Materials:Uncategorized" {
		t.Errorf("the other line changed to %q; the correction generalized", got)
	}
}

// Correcting the same line twice is two facts; the later one wins and the history survives.
func TestTheLaterAssertionWins(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	books.Categorize(log, "human", "", "a", "Acme", whole("Expenses:Unit 1", -8420))
	if err := books.Categorize(log, "human", "actually Unit 2", "a", "Acme", whole("Expenses:Unit 2", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "a").Postings[0].Account; got != "Expenses:Unit 2" {
		t.Errorf("account = %q, want the later assertion", got)
	}
}

// One charge, two properties. A split is what a real correction often is, and it is only valid if
// the postings still account for the whole line.
func TestASplitAssertionCategorizesToSeveralAccounts(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	err := books.Categorize(log, "human", "", "a", "Acme", []model.Posting{
		{Account: "Expenses:Materials:Unit 1", Amount: cad(4000)},
		{Account: "Expenses:Materials:Unit 2", Amount: cad(4420)},
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "a"); len(got.Postings) != 2 {
		t.Fatalf("got %d postings, want 2", len(got.Postings))
	}
}

// The books are the artifact, so an assertion whose postings do not account for the whole line is
// refused at write time, not discovered at render time.
func TestAnUnbalancedAssertionIsRefused(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	err := books.Categorize(log, "human", "", "a", "Acme", []model.Posting{
		{Account: "Expenses:Materials:Unit 1", Amount: cad(4000)},
	})
	if err == nil {
		t.Fatal("recorded an assertion that does not balance the line")
	}
	if !strings.Contains(err.Error(), "84.20") {
		t.Errorf("the error should say what the line was worth: %v", err)
	}

	events, _ := log.All()
	for _, e := range events {
		if e.Action == books.ActionCategorized {
			t.Fatal("an unbalanced assertion reached the log")
		}
	}
}

// One entry, one commodity, until prices exist. A posting in another commodity cannot be summed
// against the line without a price, so it is refused rather than written as a silently broken entry.
func TestAMixedCommodityAssertionIsRefused(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE")) // CAD

	err := books.Categorize(log, "human", "", "a", "Acme", []model.Posting{
		{Account: "Assets:Brokerage", Amount: model.Amount{Units: 10, Scale: 0, Commodity: "AAPL"}},
	})
	if err == nil {
		t.Fatal("recorded an entry that mixes CAD and AAPL without a price")
	}
}

// You cannot assert about a line that was never imported: there is nothing to balance against, and
// the fingerprint would orphan.
func TestCategorizingAnUnknownTransactionIsRefused(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	if err := books.Categorize(log, "human", "", "nope", "X", whole("Expenses:X", -100)); err == nil {
		t.Fatal("categorized a fingerprint that was never imported")
	}
}

// The assertion is what a rule cannot know, so it survives a rule change: fixing the rule moves
// every line except the ones a human has already spoken for.
func TestAnAssertionSurvivesARuleChange(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Materials:Uncategorized"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	books.Categorize(log, "human", "", "a", "Acme", whole("Expenses:Materials:Unit 1", -8420))

	// The rule now defaults hardware to Unit 2 for everyone.
	if _, err := books.LoadRules(log, "human", "", []rules.Rule{rule("acme", "Expenses:Materials:Unit 2")}); err != nil {
		t.Fatalf("LoadRules: %v", err)
	}

	if got := entryFor(t, log, "a").Postings[0].Account; got != "Expenses:Materials:Unit 1" {
		t.Errorf("account = %q, want the human's assertion to still hold", got)
	}
}
