package books

import (
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
	"github.com/BKPR-Pro/bkpr/lib/rules"
)

func cadAmount(cents int64) model.Amount {
	return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"}
}

func mergeTx(id, account string, day int, cents int64, description string) model.Transaction {
	return model.Transaction{
		ID: id, Account: account, Date: time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC),
		Amount: cadAmount(cents), Description: description,
	}
}

// events drains a book's log so its history can be merged elsewhere, the way importing its
// log.jsonl does.
func eventsOf(t *testing.T, log *eventlog.Log) []eventlog.Event {
	t.Helper()
	events, err := log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	return events
}

// Two books, one afterwards: the other book's lines, rules, and corrections all land, with their
// actors intact, and both books' facts fold together.
func TestMergeLogCombinesTwoBooks(t *testing.T) {
	mine := eventlog.New(eventlog.NewMemory())
	if _, err := Import(mine, "human", []model.Transaction{
		mergeTx("chq-fuel", "Assets:Bank:Chequing", 1, -6240, "SHELL GAS #123"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := AddRule(mine, "human", rules.Rule{Match: "shell", Category: "Expenses:Travel:Fuel"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	theirs := eventlog.New(eventlog.NewMemory())
	if _, err := Import(theirs, "human", []model.Transaction{
		mergeTx("card-coffee", "Liabilities:Card:Visa", 2, -500, "COFFEE HOUSE 12"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := AddRule(theirs, "human", rules.Rule{Match: "coffee", Category: "Expenses:Meals"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := Categorize(theirs, "model:claude", "", "card-coffee", "", "Coffee House", "", []model.Posting{
		{Account: "Expenses:Meals:Coffee", Amount: cadAmount(500)},
	}); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	res, err := MergeLog(mine, "human", "book-b", eventsOf(t, theirs))
	if err != nil {
		t.Fatalf("MergeLog: %v", err)
	}
	if res.Recorded == 0 || res.Repeat {
		t.Fatalf("result = %+v, want facts recorded on a first merge", res)
	}

	txs, entries, err := Ledger(mine)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("merged book holds %d lines, want both books'", len(txs))
	}
	for i, tx := range txs {
		if tx.ID == "card-coffee" && entries[i].Postings[0].Account != "Expenses:Meals:Coffee" {
			t.Errorf("the other book's correction should survive the merge, got %v", entries[i].Postings)
		}
	}

	set, err := Rules(mine)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(set) != 2 {
		t.Errorf("merged book holds %d rules, want both books'", len(set))
	}

	var actor string
	for _, e := range eventsOf(t, mine) {
		if e.Collection == CollectionTransaction && e.Action == ActionCategorized {
			actor = e.Actor
		}
	}
	if actor != "model:claude" {
		t.Errorf("the merged correction's actor = %q; who decided must survive the merge", actor)
	}
}

// A line both books saw is one line, keyed by its fingerprint, exactly as an overlapping
// statement is.
func TestMergeLogDedupesALineBothBooksSaw(t *testing.T) {
	shared := mergeTx("shared", "Assets:Bank:Chequing", 3, -1000, "THE SAME CHARGE")
	mine := eventlog.New(eventlog.NewMemory())
	theirs := eventlog.New(eventlog.NewMemory())
	for _, log := range []*eventlog.Log{mine, theirs} {
		if _, err := Import(log, "human", []model.Transaction{shared}); err != nil {
			t.Fatalf("Import: %v", err)
		}
	}

	res, err := MergeLog(mine, "human", "book-b", eventsOf(t, theirs))
	if err != nil {
		t.Fatalf("MergeLog: %v", err)
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want the shared line deduped", res.Skipped)
	}
	txs, err := Transactions(mine)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 1 {
		t.Errorf("the shared line landed %d times", len(txs))
	}
}

// Merging the same file twice is a no-op, keyed by a fingerprint of its content.
func TestMergeLogIsANoOpOnTheSameFileTwice(t *testing.T) {
	mine := eventlog.New(eventlog.NewMemory())
	theirs := eventlog.New(eventlog.NewMemory())
	if err := AddRule(theirs, "human", rules.Rule{Match: "acme", Category: "Expenses:Materials"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	foreign := eventsOf(t, theirs)

	if _, err := MergeLog(mine, "human", "book-b", foreign); err != nil {
		t.Fatalf("first MergeLog: %v", err)
	}
	before := len(eventsOf(t, mine))

	res, err := MergeLog(mine, "human", "book-b", foreign)
	if err != nil {
		t.Fatalf("second MergeLog: %v", err)
	}
	if !res.Repeat || res.Recorded != 0 {
		t.Errorf("result = %+v, want the repeat recognized and nothing recorded", res)
	}
	if after := len(eventsOf(t, mine)); after != before {
		t.Errorf("the log grew from %d to %d events on a repeat merge", before, after)
	}
}

// Each side of an internal transfer lives in its own book until the books merge; then the two
// sightings pair, and the movement books once instead of twice.
func TestMergeLogPairsATransferSeenByBothBooks(t *testing.T) {
	chequing := eventlog.New(eventlog.NewMemory())
	if _, err := Import(chequing, "human", []model.Transaction{
		mergeTx("out", "Assets:Bank:Chequing", 4, -50000, "TRANSFER TO SAVINGS"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := Categorize(chequing, "human", "", "out", "", "", "", []model.Posting{
		{Account: "Assets:Bank:Savings", Amount: cadAmount(50000)},
	}); err != nil {
		t.Fatalf("Categorize out: %v", err)
	}

	savings := eventlog.New(eventlog.NewMemory())
	if _, err := Import(savings, "human", []model.Transaction{
		mergeTx("in", "Assets:Bank:Savings", 4, 50000, "TRANSFER FROM CHEQUING"),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := Categorize(savings, "human", "", "in", "", "", "", []model.Posting{
		{Account: "Assets:Bank:Chequing", Amount: cadAmount(-50000)},
	}); err != nil {
		t.Fatalf("Categorize in: %v", err)
	}

	if _, err := MergeLog(chequing, "human", "savings-book", eventsOf(t, savings)); err != nil {
		t.Fatalf("MergeLog: %v", err)
	}
	txs, _, err := Ledger(chequing)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if len(txs) != 1 {
		t.Errorf("the merged books show the movement %d times, want once", len(txs))
	}
}

// A rule pattern both books authored folds to one rule carrying the merged (later) book's answer,
// because the pattern is the rule's identity.
func TestARulePatternBothBooksAuthoredFoldsToOneRule(t *testing.T) {
	mine := eventlog.New(eventlog.NewMemory())
	if err := AddRule(mine, "human", rules.Rule{Match: "acme", Category: "Expenses:Materials"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	theirs := eventlog.New(eventlog.NewMemory())
	if err := AddRule(theirs, "human", rules.Rule{Match: "acme", Category: "Expenses:Repairs", Payee: "Acme"}, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	if _, err := MergeLog(mine, "human", "book-b", eventsOf(t, theirs)); err != nil {
		t.Fatalf("MergeLog: %v", err)
	}
	set, err := Rules(mine)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(set) != 1 {
		t.Fatalf("got %d rules for one pattern, want one", len(set))
	}
	if set[0].Category != "Expenses:Repairs" || set[0].Payee != "Acme" {
		t.Errorf("rule = %+v, want the merged book's answer, which landed later", set[0])
	}
}
