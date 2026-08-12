package books_test

import (
	"strings"
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/model"
)

// Commenting the sole posting of a line records the note on that leg. The account need not be named
// when there is only one place it could go.
func TestCommentTheSolePosting(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	if err := books.Categorize(log, "human", "", "a", "", "Acme Hardware", "", whole("Expenses:Repairs", -8420)); err != nil {
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
	if err := books.Categorize(log, "human", "", "a", "", "Hardware", "", split); err != nil {
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
	if err := books.Categorize(log, "human", "", "a", "", "Acme Hardware", "", whole("Expenses:Repairs", -8420)); err != nil {
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
	if err := books.Categorize(log, "human", "", "a", "", "Acme Hardware", "", whole("Expenses:Repairs", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	err := books.Comment(log, "human", "", "a", "Expenses:Nope", "x", false)
	if err == nil || !strings.Contains(err.Error(), "Expenses:Nope") {
		t.Fatalf("err = %v, want a refusal naming the unknown account", err)
	}
}

// The source (elided) leg is the line's own account, the balancing posting the ledger infers rather
// than spells out. Naming it records the note on the transaction, not a categorized posting, so a
// reason for the account a movement came from is reachable the same way any other leg's note is. The
// categorized side is left untouched.
func TestCommentTheSourceLeg(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	if err := books.Categorize(log, "human", "", "a", "", "Acme Hardware", "", whole("Expenses:Repairs", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "Assets:Bank:Chequing", "paid from petty cash", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	tx, err := books.Transaction(log, "a")
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if tx.Comment != "paid from petty cash" {
		t.Errorf("source note = %q, want it recorded on the transaction's own leg", tx.Comment)
	}
	if c := entryFor(t, log, "a").Postings[0].Comment; c != "" {
		t.Errorf("the categorized posting should stay uncommented, got %q", c)
	}
}

// A note on the source leg is its own latest-wins fact, not a field frozen into the import: a later
// note wins over the one an import carried, and clearing it with -remove sticks.
func TestCommentOnTheSourceLegOverridesTheImportedNote(t *testing.T) {
	log := newLog()
	tx := line("a", 2, -8420, "ACME HARDWARE")
	tx.Comment = "carried from the file"
	importOne(t, log, tx)

	if err := books.Comment(log, "human", "", "a", "Assets:Bank:Chequing", "actually from savings", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	got, _ := books.Transaction(log, "a")
	if got.Comment != "actually from savings" {
		t.Errorf("source note = %q, want the later note to win", got.Comment)
	}

	if err := books.Comment(log, "human", "", "a", "Assets:Bank:Chequing", "", true); err != nil {
		t.Fatalf("Comment remove: %v", err)
	}
	got, _ = books.Transaction(log, "a")
	if got.Comment != "" {
		t.Errorf("source note = %q, want it cleared", got.Comment)
	}
}

// The source leg's account can also appear as a categorized posting -- a line posted back to its own
// account. Naming it is then ambiguous between the two legs, so it is refused rather than guessed.
func TestCommentRefusesWhenTheSourceAccountIsAlsoAPosting(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME HARDWARE"))
	if err := books.Categorize(log, "human", "", "a", "", "Acme", "", whole("Assets:Bank:Chequing", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "Assets:Bank:Chequing", "which leg?", false); err == nil {
		t.Fatal("naming an account that is both the source leg and a posting should be refused")
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
	if err := books.Categorize(log, "human", "", "a", "", "Hardware", "", split); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "", "which one?", false); err == nil {
		t.Fatal("a split with no account named should be refused")
	}
}

// A note is an assertion about one leg, and asserting it re-states the line as it stands -- so it
// must re-state all of it. Source routing and the invoice number are part of the line's answer, and
// dropping them silently sends the elided leg back to the account the line was imported on. Found on
// the real books: re-sourcing a card's history onto the card itself, then restoring each posting's
// note, put 174 lines back on the purpose slices they had just been moved off. With categorize unable
// to express a comment and comment unable to keep a source, the two facts could not be held at once.
func TestCommentKeepsTheLinesSourceAndInvoice(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 2, -8420, "ACME"))
	if err := books.Categorize(log, "human", "", "a", "2074", "Acme",
		"Liabilities:Acme Card", whole("Expenses:Real Estate:Materials:10 Maple Street", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if err := books.Comment(log, "human", "", "a", "", "Closet doors", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	got := entryFor(t, log, "a")
	if got.Postings[0].Comment != "Closet doors" {
		t.Errorf("comment = %q, want the note recorded", got.Postings[0].Comment)
	}
	if got.Source != "Liabilities:Acme Card" {
		t.Errorf("source = %q, want the routing to survive the note", got.Source)
	}
	if got.Invoice != "2074" {
		t.Errorf("invoice = %q, want 2074 to survive the note", got.Invoice)
	}
}
