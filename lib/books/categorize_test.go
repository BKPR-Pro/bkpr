package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
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

// A posting in another commodity needs a price to be summed against the line. Without one it cannot
// balance, so an unpriced cross-commodity assertion is still refused rather than written broken; the
// priced form (a share bought with cash) is what opens this, and is covered above.
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

// A cross-commodity assertion is now accepted when it carries a price, and the price survives the
// round trip through the log: the folded entry still balances the cash the shares cost.
func TestAPricedAssertionBuysSharesAgainstCash(t *testing.T) {
	log := newLog()
	buy := model.Transaction{
		ID: "buy", Account: "Assets:Brokerage:Cash", Date: on(2),
		Amount:      model.Amount{Units: -100000, Scale: 2, Commodity: "USD"},
		Description: "BOUGHT 10 AAPL",
	}
	importOne(t, log, buy)

	cost := model.Amount{Units: 100000, Scale: 2, Commodity: "USD"}
	err := books.Categorize(log, "human", "opened the position", "buy", "Bought Apple",
		[]model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: &cost}})
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	got := entryFor(t, log, "buy")
	if len(got.Postings) != 1 || got.Postings[0].Cost == nil {
		t.Fatalf("the price did not survive the log: %+v", got.Postings)
	}
	if !got.Balances(buy) {
		t.Error("the folded priced entry should still account for the cash")
	}
}

func brokerage(t *testing.T, log *eventlog.Log, id string, day int, cents int64, description string) model.Transaction {
	t.Helper()
	tx := model.Transaction{
		ID: id, Account: "Assets:Brokerage:Cash", Date: on(day),
		Amount:      model.Amount{Units: cents, Scale: 2, Commodity: "USD"},
		Description: description,
	}
	importOne(t, log, tx)
	return tx
}

func priced(units int64, symbol string, cents int64) model.Posting {
	cost := model.Amount{Units: cents, Scale: 2, Commodity: "USD"}
	return model.Posting{Account: "Assets:Brokerage:" + symbol, Amount: model.Amount{Units: units, Commodity: symbol}, Cost: &cost}
}

// A sale values the shares leaving at their cost base and books the difference from the proceeds as
// the gain. The base is folded from the purchase, so the sale entry is derived, not asserted: the
// gain posting is written by the fold, not by the human.
func TestASaleBooksTheGainAgainstTheCostBase(t *testing.T) {
	log := newLog()
	buy := brokerage(t, log, "buy", 1, -100000, "BOUGHT 10 AAPL")
	if err := books.Categorize(log, "human", "opened", buy.ID, "Bought Apple", []model.Posting{priced(10, "AAPL", 100000)}); err != nil {
		t.Fatalf("buy: %v", err)
	}
	sell := brokerage(t, log, "sell", 30, 120000, "SOLD 10 AAPL") // $1200 proceeds

	err := books.Sell(log, "human", "closed", sell.ID, "Sold Apple", "Income:Capital Gains",
		[]model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}}})
	if err != nil {
		t.Fatalf("Sell: %v", err)
	}

	got := entryFor(t, log, "sell")
	if !got.Balances(sell) {
		t.Fatalf("the sale should account for the proceeds: %+v", got.Postings)
	}
	var shares, gain model.Posting
	for _, p := range got.Postings {
		switch p.Account {
		case "Assets:Brokerage:AAPL":
			shares = p
		case "Income:Capital Gains":
			gain = p
		}
	}
	if shares.Cost == nil || shares.Cost.String() != "1000.00 USD" {
		t.Errorf("shares should leave at their 1000.00 USD base, got %+v", shares)
	}
	if gain.Amount.String() != "-200.00 USD" { // a $200 gain is negative in an income account
		t.Errorf("gain = %q, want -200.00 USD", gain.Amount.String())
	}
}

// The gain is a fold, not a stored number, so correcting an earlier purchase's cost base moves it.
// This is the whole reason the base is recomputed: change what the shares cost and every later
// sale's gain follows, with no touch to the sale itself.
func TestCorrectingAPurchaseMovesTheGain(t *testing.T) {
	log := newLog()
	buy := brokerage(t, log, "buy", 1, -100000, "BOUGHT 10 AAPL") // $1000 left the cash account
	books.Categorize(log, "human", "", buy.ID, "Bought Apple", []model.Posting{priced(10, "AAPL", 100000)})
	sell := brokerage(t, log, "sell", 30, 120000, "SOLD 10 AAPL") // $1200 proceeds
	books.Sell(log, "human", "", sell.ID, "Sold Apple", "Income:Capital Gains",
		[]model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}}})

	if got := gainOf(t, log, "sell"); got != "-200.00 USD" {
		t.Fatalf("gain = %q, want -200.00 USD before the correction", got)
	}

	// Of that $1000, $50 was a fee, not cost base. Re-split the buy so the shares cost $950. The
	// base drops, so the sale's gain grows to $250, and the sale line is never touched.
	fee := model.Amount{Units: 5000, Scale: 2, Commodity: "USD"}
	err := books.Categorize(log, "human", "$50 of the line was a fee", buy.ID, "Bought Apple", []model.Posting{
		priced(10, "AAPL", 95000),
		{Account: "Expenses:Brokerage:Fees", Amount: fee},
	})
	if err != nil {
		t.Fatalf("re-categorize buy: %v", err)
	}

	if got := gainOf(t, log, "sell"); got != "-250.00 USD" {
		t.Errorf("gain = %q, want -250.00 USD after the corrected base", got)
	}
}

func gainOf(t *testing.T, log *eventlog.Log, id string) string {
	t.Helper()
	for _, p := range entryFor(t, log, id).Postings {
		if p.Account == "Income:Capital Gains" {
			return p.Amount.String()
		}
	}
	t.Fatalf("no gain posting on %q", id)
	return ""
}

// A FIFO account draws a sale's base from the oldest lot, so the same two buys and sale that give a
// $200 gain under the default ACB give a different gain once the account is set to FIFO. This is the
// whole point of the setting: the policy, folded from the log, changes the base and so the gain.
func TestAFIFOAccountBooksTheOldestLotsGain(t *testing.T) {
	log := newLog()
	if err := books.SetPolicy(log, "human", "Assets:Brokerage:AAPL", "fifo"); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	b1 := brokerage(t, log, "b1", 1, -100000, "BUY 10 @ 100")
	books.Categorize(log, "human", "", b1.ID, "Buy1", []model.Posting{priced(10, "AAPL", 100000)})
	b2 := brokerage(t, log, "b2", 10, -140000, "BUY 10 @ 140")
	books.Categorize(log, "human", "", b2.ID, "Buy2", []model.Posting{priced(10, "AAPL", 140000)})
	sell := brokerage(t, log, "sell", 30, 80000, "SELL 5") // $800 proceeds

	err := books.Sell(log, "human", "", sell.ID, "Sold Apple", "Income:Capital Gains",
		[]model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 5, Commodity: "AAPL"}}})
	if err != nil {
		t.Fatalf("Sell: %v", err)
	}

	// FIFO base for 5 is the oldest lot at 100, so 500; gain is 800 - 500 = 300. ACB would be 200.
	if got := gainOf(t, log, "sell"); got != "-300.00 USD" {
		t.Errorf("gain = %q, want -300.00 USD (FIFO oldest lot)", got)
	}
}

// Selling more than the account holds is a broken book, so it is refused when the sale is asserted,
// not silently rendered.
func TestSellingMoreThanHeldIsRefused(t *testing.T) {
	log := newLog()
	buy := brokerage(t, log, "buy", 1, -100000, "BOUGHT 10 AAPL")
	books.Categorize(log, "human", "", buy.ID, "Bought Apple", []model.Posting{priced(10, "AAPL", 100000)})
	sell := brokerage(t, log, "sell", 30, 120000, "SOLD 11 AAPL")

	err := books.Sell(log, "human", "", sell.ID, "Sold Apple", "Income:Capital Gains",
		[]model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 11, Commodity: "AAPL"}}})
	if err == nil {
		t.Fatal("sold more shares than were held")
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
	if err := books.SetRule(log, "human", "", rule("acme", "Expenses:Materials:Unit 2")); err != nil {
		t.Fatalf("SetRule: %v", err)
	}

	if got := entryFor(t, log, "a").Postings[0].Account; got != "Expenses:Materials:Unit 1" {
		t.Errorf("account = %q, want the human's assertion to still hold", got)
	}
}

// A line that arrived already categorized (a ledger file names its own postings) carries that
// categorization in as an assertion, so folding shows the file's account with no rule and no manual
// categorize step. Without the carry, and with no rule, the line would fall to Uncategorized.
func TestCarryCategorizationsAssertsTheFilesCategorization(t *testing.T) {
	log := newLog()
	tx := line("a", 1, -8420, "ACME HARDWARE")
	importOne(t, log, tx)

	entry := model.Entry{Payee: "Acme Hardware", Postings: whole("Expenses:Materials", -8420)}
	carried, skipped, err := books.CarryCategorizations(log, "import:acct.txt", "from the ledger file",
		[]model.Transaction{tx}, []model.Entry{entry})
	if err != nil {
		t.Fatalf("CarryCategorizations: %v", err)
	}
	if carried != 1 || skipped != 0 {
		t.Errorf("got carried=%d skipped=%d, want 1 and 0", carried, skipped)
	}

	got := entryFor(t, log, "a")
	if got.Postings[0].Account != "Expenses:Materials" {
		t.Errorf("account = %q, want the file's category, not Uncategorized", got.Postings[0].Account)
	}
	if got.Payee != "Acme Hardware" {
		t.Errorf("payee = %q, want the file's payee", got.Payee)
	}
}

// A carried categorization is an ordinary assertion, so a later human correction still wins over it,
// exactly as it would over a rule.
func TestACarriedCategorizationIsOverriddenByAHuman(t *testing.T) {
	log := newLog()
	tx := line("a", 1, -8420, "ACME HARDWARE")
	importOne(t, log, tx)
	books.CarryCategorizations(log, "import:acct.txt", "", []model.Transaction{tx},
		[]model.Entry{{Payee: "Acme", Postings: whole("Expenses:Materials", -8420)}})

	if err := books.Categorize(log, "human", "receipt was Unit 1", "a", "Acme",
		whole("Expenses:Materials:Unit 1", -8420)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if got := entryFor(t, log, "a").Postings[0].Account; got != "Expenses:Materials:Unit 1" {
		t.Errorf("account = %q, want the human's correction over the carried one", got)
	}
}

// A categorization that cannot post against the line — a mixed-commodity placeholder the books
// cannot balance — is left to the rules rather than asserted, so the import records what it can and
// the line stays honestly Uncategorized instead of carrying a broken entry.
func TestCarryCategorizationsSkipsAnEntryThatDoesNotBalance(t *testing.T) {
	log := newLog()
	tx := line("a", 1, -10000, "PROPERTY PURCHASE")
	importOne(t, log, tx)

	entry := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Prop", Amount: model.Amount{Units: 1, Commodity: "Property"}},
	}}
	carried, skipped, err := books.CarryCategorizations(log, "import:acct.txt", "",
		[]model.Transaction{tx}, []model.Entry{entry})
	if err != nil {
		t.Fatalf("CarryCategorizations: %v", err)
	}
	if carried != 0 || skipped != 1 {
		t.Errorf("got carried=%d skipped=%d, want 0 and 1", carried, skipped)
	}
	if !entryFor(t, log, "a").Uncategorized() {
		t.Error("the unbalanced entry should be left uncategorized, not asserted")
	}
}
