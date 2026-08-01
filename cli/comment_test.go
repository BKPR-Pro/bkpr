package main

import (
	"testing"

	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/store"
)

// lastActorOf returns the actor on the most recent event of the given collection and action, since a
// comment re-asserts the categorization and so is the latest such event, not the first.
func lastActorOf(t *testing.T, collection, action string) string {
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
	actor := ""
	for _, e := range events {
		if e.Collection == collection && e.Action == action {
			actor = e.Actor
		}
	}
	if actor == "" {
		t.Fatalf("no %s.%s event", collection, action)
	}
	return actor
}

// entryFor folds the store and returns the entry carried by the given transaction id.
func entryFor(t *testing.T, id string) model.Entry {
	t.Helper()
	txs, entries, _ := foldStore(t)
	for i, tx := range txs {
		if tx.ID == id {
			return entries[i]
		}
	}
	t.Fatalf("no entry for %q", id)
	return model.Entry{}
}

// The comment command records a note on a line's sole posting, and the actor who left it, so the
// log says who annotated the books.
func TestCommentRecordsTheNoteAndActor(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := categorize([]string{"tx1", "-category", "Expenses:Food"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	if err := commentCmd([]string{"tx1", "-text", "team lunch", "-actor", "model:claude"}); err != nil {
		t.Fatalf("comment: %v", err)
	}

	if got := entryFor(t, "tx1").Postings[0].Comment; got != "team lunch" {
		t.Errorf("comment = %q, want the note recorded", got)
	}
	if got := lastActorOf(t, "transaction", "categorized"); got != "model:claude" {
		t.Errorf("actor = %q, want model:claude", got)
	}
}

// Naming the line's own account notes the source (elided) leg, the balancing posting the ledger
// infers. The note lands on the transaction, not a categorized posting, and reads back on the fold.
func TestCommentTheSourceLeg(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := categorize([]string{"tx1", "-category", "Expenses:Food"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	if err := commentCmd([]string{"tx1", "-account", "Assets:Bank:Chequing", "-text", "paid from petty cash"}); err != nil {
		t.Fatalf("comment: %v", err)
	}

	txs, _, _ := foldStore(t)
	var got string
	for _, tx := range txs {
		if tx.ID == "tx1" {
			got = tx.Comment
		}
	}
	if got != "paid from petty cash" {
		t.Errorf("source note = %q, want it recorded on the transaction's own leg", got)
	}
}

// -remove clears the note from the posting.
func TestCommentRemoveClearsTheNote(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := categorize([]string{"tx1", "-category", "Expenses:Food"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	if err := commentCmd([]string{"tx1", "-text", "team lunch"}); err != nil {
		t.Fatalf("comment: %v", err)
	}

	if err := commentCmd([]string{"tx1", "-remove"}); err != nil {
		t.Fatalf("comment -remove: %v", err)
	}

	if got := entryFor(t, "tx1").Postings[0].Comment; got != "" {
		t.Errorf("comment = %q, want it removed", got)
	}
}

// -text and -remove are opposite intents, so asking for both is refused rather than guessed.
func TestCommentRefusesTextWithRemove(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := categorize([]string{"tx1", "-category", "Expenses:Food"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	if err := commentCmd([]string{"tx1", "-text", "x", "-remove"}); err == nil {
		t.Fatal("giving -text and -remove together should be refused")
	}
}

// Neither -text nor -remove leaves nothing to do, so it is refused.
func TestCommentRequiresTextOrRemove(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")
	if err := categorize([]string{"tx1", "-category", "Expenses:Food"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	if err := commentCmd([]string{"tx1"}); err == nil {
		t.Fatal("a comment with neither -text nor -remove should be refused")
	}
}
