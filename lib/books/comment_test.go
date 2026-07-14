package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
)

// Commenting the sole posting of a line records the note on that leg. The account need not be named
// when there is only one place it could go.
func TestCommentTheSolePosting(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	if err := books.Categorize(log, "human", "", "a", "Acme Hardware", whole("Expenses:Repairs", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "", "receipt in the glovebox", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	if got := entryFor(t, log, "a").Postings[0].Comment; got != "receipt in the glovebox" {
		t.Errorf("comment = %q, want the note recorded on the posting", got)
	}
}

// A split needs the account named, because the note belongs to one leg. The named leg gets the
// comment and the others are untouched.
func TestCommentNamesOneLegOfASplit(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -10000, "HARDWARE"))
	split := []model.Posting{
		{Account: "Expenses:A", Amount: cad(4000)},
		{Account: "Expenses:B", Amount: cad(6000)},
	}
	if err := books.Categorize(log, "human", "", "a", "Hardware", split); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "Expenses:B", "the larger half", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	e := entryFor(t, log, "a")
	if e.Postings[0].Comment != "" {
		t.Errorf("the unnamed leg should stay uncommented, got %q", e.Postings[0].Comment)
	}
	if e.Postings[1].Comment != "the larger half" {
		t.Errorf("the named leg should carry the note, got %q", e.Postings[1].Comment)
	}
}

// Removing clears the note from the leg. The rest of the entry is unchanged.
func TestCommentRemoveClearsTheNote(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	if err := books.Categorize(log, "human", "", "a", "Acme Hardware", whole("Expenses:Repairs", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if err := books.Comment(log, "human", "", "a", "", "note to drop", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "", "", true); err != nil {
		t.Fatalf("Comment remove: %v", err)
	}

	e := entryFor(t, log, "a")
	if e.Postings[0].Comment != "" {
		t.Errorf("comment = %q, want it removed", e.Postings[0].Comment)
	}
	if e.Postings[0].Account != "Expenses:Repairs" {
		t.Errorf("removing a note should not disturb the account, got %q", e.Postings[0].Account)
	}
}

// A note on a rule-categorized line freezes the rule's answer for that one line, because a comment
// is an assertion about a specific line the way a correction is. The account is carried unchanged.
func TestCommentOnARuleCategorizedLineAsserts(t *testing.T) {
	log := newLog()
	loaded(t, log, rule("acme", "Expenses:Repairs"))
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))

	if err := books.Comment(log, "human", "", "a", "", "keep an eye on this vendor", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	e := entryFor(t, log, "a")
	if e.Postings[0].Account != "Expenses:Repairs" || e.Postings[0].Comment != "keep an eye on this vendor" {
		t.Errorf("posting = %+v, want the rule's account with the note", e.Postings[0])
	}
}

// Naming an account no posting carries is refused, so a typo is caught rather than silently
// recording nothing.
func TestCommentRejectsAnUnknownAccount(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	if err := books.Categorize(log, "human", "", "a", "Acme Hardware", whole("Expenses:Repairs", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	err := books.Comment(log, "human", "", "a", "Expenses:Nope", "x", false)
	if err == nil || !strings.Contains(err.Error(), "Expenses:Nope") {
		t.Fatalf("err = %v, want a refusal naming the unknown account", err)
	}
}

// A split with no account named is ambiguous: the note could belong to either leg, so it is
// refused rather than guessed.
func TestCommentRefusesAnAmbiguousSplit(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -10000, "HARDWARE"))
	split := []model.Posting{
		{Account: "Expenses:A", Amount: cad(4000)},
		{Account: "Expenses:B", Amount: cad(6000)},
	}
	if err := books.Categorize(log, "human", "", "a", "Hardware", split); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "", "which one?", false); err == nil {
		t.Fatal("a split with no account named should be refused")
	}
}
