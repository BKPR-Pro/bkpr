package books_test

import (
	"testing"
	"time"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/rules"
)

// overlayRule is a taxed vendor rule that has opted into the overlay: TaxFrom is set, so its tax
// reaches lines something else categorized, from that date on.
func overlayRule() rules.Rule {
	return rules.Rule{Match: "kent", TaxRate: "15%", TaxAccount: "Assets:HST ITC", TaxFrom: "2026-01-01"}
}

func dated(id string, date string, cents int64, description string) model.Transaction {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	return model.Transaction{
		ID: id, Account: "Liabilities:Card:Visa", Date: parsed,
		Amount: cad(cents), Description: description,
	}
}

// The motivating case: unit attribution is a manual decision, so every line is categorized by hand
// and no vendor rule can carry the category. The vendor's tax still applies — the overlay splits
// the human's category leg, so the human keeps the decision and the arithmetic never depends on
// discipline.
func TestATaxOverlayReachesAnExplicitlyCategorizedLine(t *testing.T) {
	log := newLog()
	loaded(t, log, overlayRule())
	tx := dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES")
	importOne(t, log, tx)

	err := books.Categorize(log, "human", "receipt says Unit 1", "a", "", "Kent", "",
		whole("Expenses:Materials:Unit 1", -11500))
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	got := entryFor(t, log, "a")
	if len(got.Postings) != 2 {
		t.Fatalf("want the asserted leg split, got %+v", got.Postings)
	}
	if got.Postings[0].Account != "Expenses:Materials:Unit 1" || got.Postings[0].Amount.String() != "100.00 CAD" {
		t.Errorf("net = %s %s, want the human's category at 100.00 CAD", got.Postings[0].Account, got.Postings[0].Amount)
	}
	if got.Postings[1].Account != "Assets:HST ITC" || got.Postings[1].Amount.String() != "15.00 CAD" {
		t.Errorf("tax = %s %s, want Assets:HST ITC at 15.00 CAD", got.Postings[1].Account, got.Postings[1].Amount)
	}
	if !got.Balances(tx) {
		t.Error("the overlaid entry must still account for the whole line")
	}
}

// A taxed rule that has not opted in behaves exactly as it always has: an assertion replaces the
// rule's answer whole, tax included. Existing books fold identically.
func TestARuleWithoutTaxFromNeverOverlays(t *testing.T) {
	log := newLog()
	r := overlayRule()
	r.TaxFrom = ""
	loaded(t, log, r)
	importOne(t, log, dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES"))

	if err := books.Categorize(log, "human", "", "a", "", "Kent", "", whole("Expenses:Materials:Unit 1", -11500)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "a"); len(got.Postings) != 1 {
		t.Errorf("without TaxFrom the assertion stands whole, got %+v", got.Postings)
	}
}

// Postings spelled out leg by leg — categorize's -post form — outrank the overlay even when the
// shape looks like a plain category: a deliberate no-split stays a no-split.
func TestSpelledPostsOutrankTheOverlay(t *testing.T) {
	log := newLog()
	loaded(t, log, overlayRule())
	importOne(t, log, dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES"))

	err := books.CategorizePosts(log, "human", "the accountant said keep it gross", "a", "", "Kent", "",
		whole("Expenses:Materials:Unit 1", -11500))
	if err != nil {
		t.Fatalf("CategorizePosts: %v", err)
	}

	got := entryFor(t, log, "a")
	if len(got.Postings) != 1 || got.Postings[0].Amount.String() != "115.00 CAD" {
		t.Errorf("spelled posts must stand whole, got %+v", got.Postings)
	}
}

// A hand-made split is the caller's arithmetic, so the overlay leaves it alone.
func TestAHandMadeSplitOutranksTheOverlay(t *testing.T) {
	log := newLog()
	loaded(t, log, overlayRule())
	importOne(t, log, dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES"))

	err := books.Categorize(log, "human", "", "a", "", "Kent", "", []model.Posting{
		{Account: "Expenses:Materials:Unit 1", Amount: cad(5000)},
		{Account: "Expenses:Materials:Unit 2", Amount: cad(6500)},
	})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	got := entryFor(t, log, "a")
	if len(got.Postings) != 2 || got.Postings[0].Amount.String() != "50.00 CAD" {
		t.Errorf("a hand-made split must stay the human's, got %+v", got.Postings)
	}
}

// A line asserted straight to the tax account already carries the tax, so it is never split again.
func TestTheOverlayNeverSplitsTwice(t *testing.T) {
	log := newLog()
	loaded(t, log, overlayRule())
	importOne(t, log, dated("a", "2026-03-05", 1500, "KENT REFUND"))

	if err := books.Categorize(log, "human", "an ITC refund leg", "a", "", "Kent", "", whole("Assets:HST ITC", 1500)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "a"); len(got.Postings) != 1 {
		t.Errorf("a leg already on the tax account must not split, got %+v", got.Postings)
	}
}

// The overlay is bounded by the rule's TaxFrom: a filed year's line, already split in the ledger it
// was carried from, stays exactly as asserted.
func TestTheOverlayIsBoundedByDate(t *testing.T) {
	log := newLog()
	loaded(t, log, overlayRule())
	importOne(t, log, dated("a", "2025-06-30", -11500, "KENT BUILDING SUPPLIES"))

	if err := books.Categorize(log, "human", "", "a", "", "Kent", "", whole("Expenses:Materials:Unit 1", -11500)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "a"); len(got.Postings) != 1 {
		t.Errorf("a line before TaxFrom must stay as asserted, got %+v", got.Postings)
	}
}

// A categorization carried from a ledger file spells the file's own legs, so the overlay never
// restates it — a re-imported historical book keeps its splits byte for byte.
func TestACarriedCategorizationOutranksTheOverlay(t *testing.T) {
	log := newLog()
	loaded(t, log, overlayRule())
	tx := dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES")
	importOne(t, log, tx)

	entry := model.Entry{Payee: "Kent", Postings: whole("Expenses:Materials:Unit 1", -11500)}
	if _, _, err := books.CarryCategorizations(log, "import:acct.txt", "", []model.Transaction{tx}, []model.Entry{entry}); err != nil {
		t.Fatalf("CarryCategorizations: %v", err)
	}

	got := entryFor(t, log, "a")
	if len(got.Postings) != 1 || got.Postings[0].Amount.String() != "115.00 CAD" {
		t.Errorf("a carried categorization must stand whole, got %+v", got.Postings)
	}
}

// End to end, the category decides the treatment: the same vendor's charge categorized to the
// claimable property extracts to that property's tax account, and one categorized to the other
// property stays gross until the accountant rules.
func TestTheOverlayScopesByTheAssertedCategory(t *testing.T) {
	log := newLog()
	scoped := rules.Rule{
		Match: "kent", TaxRate: "15%", TaxFrom: "2026-01-01",
		TaxCategory: `Materials:(9 Schoodic)`, TaxAccount: "Expenses:Real Estate:HST:ITC:$1",
	}
	loaded(t, log, scoped)
	importOne(t, log, dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES"))
	importOne(t, log, dated("b", "2026-03-06", -11500, "KENT BUILDING SUPPLIES"))

	if err := books.Categorize(log, "human", "", "a", "", "Kent", "", whole("Expenses:Real Estate:Materials:9 Schoodic", -11500)); err != nil {
		t.Fatalf("Categorize a: %v", err)
	}
	if err := books.Categorize(log, "human", "", "b", "", "Kent", "", whole("Expenses:Real Estate:Materials:22 Lisgar", -11500)); err != nil {
		t.Fatalf("Categorize b: %v", err)
	}

	claimed := entryFor(t, log, "a")
	if len(claimed.Postings) != 2 || claimed.Postings[1].Account != "Expenses:Real Estate:HST:ITC:9 Schoodic" {
		t.Errorf("the claimable property should split to its derived account, got %+v", claimed.Postings)
	}
	if gross := entryFor(t, log, "b"); len(gross.Postings) != 1 {
		t.Errorf("the other property must stay gross, got %+v", gross.Postings)
	}
}

// A note frozen onto a spelled assertion re-asserts it, and the freeze must not soften it: the legs
// were the caller's arithmetic before the note and stay so after, even against a later overlay.
func TestACommentKeepsSpelledPostsOutOfTheOverlay(t *testing.T) {
	log := newLog()
	importOne(t, log, dated("a", "2026-03-05", -11500, "KENT BUILDING SUPPLIES"))
	if err := books.CategorizePosts(log, "human", "keep it gross", "a", "", "Kent", "",
		whole("Expenses:Materials:Unit 1", -11500)); err != nil {
		t.Fatalf("CategorizePosts: %v", err)
	}
	if err := books.Comment(log, "human", "", "a", "", "the accountant's ruling is pending", false); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	loaded(t, log, overlayRule())

	got := entryFor(t, log, "a")
	if len(got.Postings) != 1 || got.Postings[0].Amount.String() != "115.00 CAD" {
		t.Errorf("the commented spelled posts must stand whole, got %+v", got.Postings)
	}
	if got.Postings[0].Comment != "the accountant's ruling is pending" {
		t.Errorf("comment = %q, want the note kept", got.Postings[0].Comment)
	}
}
