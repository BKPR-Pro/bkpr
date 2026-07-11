package main

import (
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
	"github.com/dallasread/bookkeeper/lib/store"
)

// bookHere creates an empty book in a temp dir and chdirs into it, so the commands (which open the
// book at ".") run against it.
func bookHere(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if _, err := store.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(dir)
}

// actorOf returns the actor recorded on the first event of the given collection and action.
func actorOf(t *testing.T, collection, action string) string {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	events, err := s.Log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, e := range events {
		if e.Collection == collection && e.Action == action {
			return e.Actor
		}
	}
	t.Fatalf("no %s.%s event", collection, action)
	return ""
}

func seedTx(t *testing.T, id string) {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = books.Import(s.Log, "statement:test", []model.Transaction{{
		ID: id, Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: -1000, Scale: 2, Commodity: "CAD"}, Description: "THING",
	}})
	s.Close()
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
}

// A model drives the same categorize path a person does, so the log records who answered: -actor
// carries the name of whoever made the decision.
func TestCategorizeRecordsTheActor(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	if err := categorize([]string{"-tx", "tx1", "-category", "Expenses:Food", "-actor", "model:claude"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	if got := actorOf(t, "transaction", "categorized"); got != "model:claude" {
		t.Errorf("actor = %q, want model:claude", got)
	}
}

// The default is a person, so an unattributed categorize reads as human rather than blank.
func TestCategorizeDefaultsTheActorToHuman(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	if err := categorize([]string{"-tx", "tx1", "-category", "Expenses:Food"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	if got := actorOf(t, "transaction", "categorized"); got != "human" {
		t.Errorf("actor = %q, want human by default", got)
	}
}

func TestVoidRecordsTheActor(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	if err := voidCmd([]string{"-tx", "tx1", "-actor", "model:claude"}); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := actorOf(t, "transaction", "voided"); got != "model:claude" {
		t.Errorf("actor = %q, want model:claude", got)
	}
}

func TestRulesSetRecordsTheActor(t *testing.T) {
	bookHere(t)

	if err := ruleSetOne([]string{"-match", "acme", "-category", "Expenses:Materials", "-actor", "model:claude"}); err != nil {
		t.Fatalf("rules set: %v", err)
	}
	if got := actorOf(t, "rule", "added"); got != "model:claude" {
		t.Errorf("actor = %q, want model:claude", got)
	}
}

// The match command forces a pairing and records who decided.
func TestMatchRecordsTheActor(t *testing.T) {
	bookHere(t)
	seedTx(t, "a")
	seedTx(t, "b")

	if err := match([]string{"-tx", "a", "-with", "b", "-actor", "model:claude"}); err != nil {
		t.Fatalf("match: %v", err)
	}
	if got := actorOf(t, "transaction", "matched"); got != "model:claude" {
		t.Errorf("actor = %q, want model:claude", got)
	}
}

// match refuses an ambiguous request: exactly one of -with or -break.
func TestMatchRefusesBothOrNeither(t *testing.T) {
	bookHere(t)
	seedTx(t, "a")
	if err := match([]string{"-tx", "a"}); err == nil {
		t.Error("neither -with nor -break should be an error")
	}
	if err := match([]string{"-tx", "a", "-with", "b", "-break"}); err == nil {
		t.Error("both -with and -break should be an error")
	}
}

// upsertRule threads the actor through both the add and change paths.
func TestUpsertRuleRecordsTheActor(t *testing.T) {
	log := ruleLog(t)

	err := upsertRule(log, rules.Rule{Match: "acme", Category: "Expenses:X"}, map[string]bool{"category": true}, "", "", "model:claude")
	if err != nil {
		t.Fatalf("upsertRule: %v", err)
	}
	events, _ := log.All()
	for _, e := range events {
		if e.Collection == "rule" && e.Action == "added" {
			if e.Actor != "model:claude" {
				t.Errorf("actor = %q, want model:claude", e.Actor)
			}
			return
		}
	}
	t.Fatal("no rule.added event")
}
