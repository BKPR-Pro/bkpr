package books_test

import (
	"testing"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/costbasis"
	"github.com/dallasread/bkpr/lib/eventlog"
)

func policies(t *testing.T, log *eventlog.Log) books.PolicySet {
	t.Helper()
	ps, err := books.Policies(log)
	if err != nil {
		t.Fatalf("Policies: %v", err)
	}
	return ps
}

// Nothing set means ACB, the rule for Canadian capital property and the default this book is built
// around. It is the base every other setting overrides.
func TestThePolicyDefaultsToACB(t *testing.T) {
	log := newLog()
	if got := policies(t, log).Method("Assets:Brokerage:AAPL"); got != "acb" {
		t.Errorf("default policy = %q, want acb", got)
	}
}

// The book-wide default can be set once, and then every account without its own setting uses it.
func TestTheBookDefaultCanBeChanged(t *testing.T) {
	log := newLog()
	if err := books.SetPolicy(log, "human", "", "fifo"); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	if got := policies(t, log).Method("Assets:Brokerage:AAPL"); got != "fifo" {
		t.Errorf("policy = %q, want the new default fifo", got)
	}
}

// A per-account setting overrides the default for that account only, so a US account can run FIFO in
// the same book a Canadian default keeps on ACB.
func TestAnAccountOverridesTheDefault(t *testing.T) {
	log := newLog()
	books.SetPolicy(log, "human", "", "acb")                     // the book default
	books.SetPolicy(log, "human", "Assets:Brokerage:US", "fifo") // one account differs

	ps := policies(t, log)
	if got := ps.Method("Assets:Brokerage:US"); got != "fifo" {
		t.Errorf("US account = %q, want its fifo override", got)
	}
	if got := ps.Method("Assets:Brokerage:CA"); got != "acb" {
		t.Errorf("other account = %q, want the acb default", got)
	}
}

// The setting is a fact in the log, so restating it is another fact and the latest wins.
func TestTheLatestPolicyWins(t *testing.T) {
	log := newLog()
	books.SetPolicy(log, "human", "", "acb")
	books.SetPolicy(log, "human", "", "fifo")
	if got := policies(t, log).Method("X"); got != "fifo" {
		t.Errorf("policy = %q, want the latest fifo", got)
	}
}

// An unknown method is refused when set, not silently defaulted, and nothing reaches the log.
func TestAnUnknownPolicyIsRefused(t *testing.T) {
	log := newLog()
	if err := books.SetPolicy(log, "human", "", "lifo"); err == nil {
		t.Fatal("recorded an unknown cost-basis policy")
	}
	events, _ := log.All()
	for _, e := range events {
		if e.Collection == books.CollectionPolicy {
			t.Fatal("an unknown policy reached the log")
		}
	}
}

// For resolves a name to the policy value the fold uses, so an account's method drives its base.
func TestForReturnsTheSelectedPolicy(t *testing.T) {
	log := newLog()
	books.SetPolicy(log, "human", "Assets:Brokerage:US", "fifo")
	if got := policies(t, log).For("Assets:Brokerage:US"); got.Name() != costbasis.FIFO.Name() {
		t.Errorf("For = %q, want fifo", got.Name())
	}
}
