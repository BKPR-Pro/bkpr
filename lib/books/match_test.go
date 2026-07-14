package books_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

func lineIn(id, account string, day int, cents int64, description string) model.Transaction {
	tx := line(id, day, cents, description)
	tx.Account = account
	return tx
}

// A transfer set up so both statements name the other account: chequing sends to savings, savings
// receives from chequing.
func transferBooks(t *testing.T) *eventlog.Log {
	t.Helper()
	log := newLog()
	loaded(t, log,
		rule("to savings", "Assets:Bank:Savings"),
		rule("from chequing", "Assets:Bank:Chequing"),
	)
	return log
}

// The heart of it. One movement, seen in two statements, books once. Booking both would move the
// money out of chequing and then back in, netting chequing to zero when it really fell by 500.
func TestATransferSeenInBothStatementsBooksOnce(t *testing.T) {
	log := transferBooks(t)
	importOne(t, log, line("c", 1, -50000, "TRANSFER TO SAVINGS"))
	importOne(t, log, lineIn("s", "Assets:Bank:Savings", 1, 50000, "TRANSFER FROM CHEQUING"))

	txs, entries := ledger(t, log)

	if len(txs) != 1 {
		t.Fatalf("got %d entries, want 1: the transfer was booked twice", len(txs))
	}
	// The surviving entry still moves money between the two accounts.
	if !entries[0].Balances(txs[0]) {
		t.Errorf("the kept entry does not balance: %+v", entries[0])
	}
}

// The earlier sighting is kept, so the entry is dated at the movement's origin and the choice is
// deterministic across runs.
func TestTheEarlierSightingIsTheOneKept(t *testing.T) {
	log := transferBooks(t)
	importOne(t, log, line("c", 1, -50000, "TRANSFER TO SAVINGS"))
	importOne(t, log, lineIn("s", "Assets:Bank:Savings", 3, 50000, "TRANSFER FROM CHEQUING"))

	txs, _ := ledger(t, log)
	if len(txs) != 1 || txs[0].ID != "c" {
		t.Fatalf("kept %v, want the earlier chequing line", txs)
	}
}

// Suppression is not "two lines that happen to cancel out". Both sides must name the other's
// account, or a real expense and a coincidental deposit of the same size would vanish.
func TestOppositeAmountsThatAreNotMutualTransfersBookBoth(t *testing.T) {
	log := newLog()
	loaded(t, log,
		rule("groceries", "Expenses:Food"),
		rule("paycheck", "Income:Salary"),
	)
	importOne(t, log, line("c", 1, -50000, "GROCERIES"))
	importOne(t, log, lineIn("s", "Assets:Bank:Savings", 1, 50000, "PAYCHECK"))

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("got %d entries, want 2: an expense and a deposit are not a transfer", len(txs))
	}
}

// Only one leg imported (the other account's statement has not been imported) books normally; there
// is nothing to double.
func TestAnUnpairedTransferBooksNormally(t *testing.T) {
	log := transferBooks(t)
	importOne(t, log, line("c", 1, -50000, "TRANSFER TO SAVINGS"))

	if txs, _ := ledger(t, log); len(txs) != 1 {
		t.Fatalf("got %d entries, want 1", len(txs))
	}
}

// Two sightings dated too far apart are not assumed to be the same movement.
func TestSightingsOutsideTheWindowDoNotPair(t *testing.T) {
	log := transferBooks(t)
	importOne(t, log, line("c", 1, -50000, "TRANSFER TO SAVINGS"))
	importOne(t, log, lineIn("s", "Assets:Bank:Savings", 20, 50000, "TRANSFER FROM CHEQUING"))

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("got %d entries, want 2: sightings 19 days apart are not one movement", len(txs))
	}
}

// Two separate transfers of the same size in the same window book as two, not collapsed to one:
// the counts on each side match, so each duplicate finds a partner and each real movement survives.
func TestTwoRealTransfersOfTheSameSizeBothSurvive(t *testing.T) {
	log := transferBooks(t)
	importOne(t, log, line("c1", 1, -50000, "TRANSFER TO SAVINGS"))
	importOne(t, log, line("c2", 2, -50000, "TRANSFER TO SAVINGS"))
	importOne(t, log, lineIn("s1", "Assets:Bank:Savings", 1, 50000, "TRANSFER FROM CHEQUING"))
	importOne(t, log, lineIn("s2", "Assets:Bank:Savings", 2, 50000, "TRANSFER FROM CHEQUING"))

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("got %d entries, want 2: two movements, each seen twice", len(txs))
	}
}

// A real transfer whose two sightings fall outside the pairing window books twice, because the fold
// will not assume two lines a fortnight apart are one movement. Forcing the match tells it they are,
// and the later one is suppressed.
func TestForcingAMatchSuppressesTheLaterSighting(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -50000, "MOVED OUT"))
	importOne(t, log, lineIn("b", "Assets:Bank:Savings", 15, 50000, "MOVED IN"))

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("precondition: sightings 14 days apart book twice, got %d", len(txs))
	}

	if err := books.Match(log, "human", "a", "b", true); err != nil {
		t.Fatalf("Match: %v", err)
	}
	txs, _ := ledger(t, log)
	if len(txs) != 1 || txs[0].ID != "a" {
		t.Fatalf("want only the earlier sighting a, got %d entries", len(txs))
	}
}

// The hands-off case: two unclaimed sightings, the same amount moving the other way between two of
// your accounts inside the window, pair by themselves with no rule and no naming. The kept leg is
// booked as the transfer, so both accounts' balances are right.
func TestUnclaimedOppositeSightingsAutoPair(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -50000, "E-TRANSFER"))
	importOne(t, log, lineIn("b", "Assets:Bank:Savings", 2, 50000, "E-TRANSFER"))

	txs, entries := ledger(t, log)
	if len(txs) != 1 || txs[0].ID != "a" {
		t.Fatalf("want the pair collapsed to the earlier leg, got %d entries", len(txs))
	}
	if !entries[0].Balances(txs[0]) {
		t.Errorf("the kept entry does not balance: %+v", entries[0])
	}
	if len(entries[0].Postings) != 1 || entries[0].Postings[0].Account != "Assets:Bank:Savings" {
		t.Errorf("the kept leg should book to the other account, got %+v", entries[0].Postings)
	}
}

// Breaking the automatic pairing keeps both unclaimed sightings, each booking on its own.
func TestBreakingAnAutoPairKeepsBothUnclaimedSightings(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -50000, "E-TRANSFER"))
	importOne(t, log, lineIn("b", "Assets:Bank:Savings", 2, 50000, "E-TRANSFER"))

	if txs, _ := ledger(t, log); len(txs) != 1 {
		t.Fatalf("precondition: the fold auto-pairs these, got %d", len(txs))
	}
	if err := books.Match(log, "human", "b", "", false); err != nil {
		t.Fatalf("Match break: %v", err)
	}
	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("a broken auto-pair should keep both, got %d", len(txs))
	}
}

// The automatic fold can pair two lines that only look like a transfer. Breaking the match keeps
// both, telling the fold this sighting is not a duplicate.
func TestBreakingAMatchKeepsBothSightings(t *testing.T) {
	log := transferBooks(t)
	importOne(t, log, line("c", 1, -50000, "TRANSFER TO SAVINGS"))
	importOne(t, log, lineIn("s", "Assets:Bank:Savings", 3, 50000, "TRANSFER FROM CHEQUING"))

	if txs, _ := ledger(t, log); len(txs) != 1 {
		t.Fatalf("precondition: the fold pairs these, got %d", len(txs))
	}

	if err := books.Match(log, "human", "s", "", false); err != nil {
		t.Fatalf("Match break: %v", err)
	}
	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("a broken match should keep both, got %d", len(txs))
	}
}

// Two lines that are explicitly categorized -- carried in from a hand-kept ledger, or corrected by
// hand -- are statements of fact, not unclaimed sightings to be guessed at. When two such asserted
// entries happen to be the same size moving opposite ways between owned accounts inside the window
// but do not name each other, the loose fold must not fuse them: doing so deletes a real line and
// wipes the surviving one's payee. This is the paycheck-vs-credit-card-payment coincidence from the
// real books: a $1000 paycheck and an unrelated $1000 card payment five days apart.
func TestTwoAssertedOppositeLinesAreNotFusedAsATransfer(t *testing.T) {
	log := newLog()
	// The card account must be owned for the loose fold to consider the pair at all, so a card
	// statement line anchors it as a source account. Its size and date keep it clear of the pair.
	importOne(t, log, lineIn("card", "Liabilities:Card", 25, -7300, "CARD STATEMENT"))
	importOne(t, log, lineIn("pay", "Liabilities:Shareholder Loan", 7, 100000, "Dallas Read"))
	importOne(t, log, lineIn("bill", "Assets:Bank:Chequing", 12, -100000, "RBC Mastercard"))

	// The paycheck: money left chequing into the shareholder loan, with a note on the leg.
	if err := books.Categorize(log, "human", "carried", "pay", "", "Dallas Read",
		[]model.Posting{{Account: "Assets:Bank:Chequing", Amount: cad(-100000), Comment: "Paycheque"}}); err != nil {
		t.Fatalf("Categorize pay: %v", err)
	}
	// The card payment: money left chequing to the card. It names the card, not the shareholder loan,
	// so the two do not mutually name each other and only the loose fold could pair them.
	if err := books.Categorize(log, "human", "carried", "bill", "", "RBC Mastercard",
		[]model.Posting{{Account: "Liabilities:Card", Amount: cad(100000)}}); err != nil {
		t.Fatalf("Categorize bill: %v", err)
	}

	txs, entries := ledger(t, log)
	ids := map[string]bool{}
	for _, tx := range txs {
		ids[tx.ID] = true
	}
	if !ids["pay"] || !ids["bill"] {
		t.Fatalf("both asserted lines must survive; got %v", ids)
	}
	for i, tx := range txs {
		if tx.ID == "pay" {
			if entries[i].Payee != "Dallas Read" {
				t.Errorf("paycheck payee wiped to %q, want it kept", entries[i].Payee)
			}
			if len(entries[i].Postings) != 1 || entries[i].Postings[0].Comment != "Paycheque" {
				t.Errorf("paycheck leg/comment lost: %+v", entries[i].Postings)
			}
		}
	}
}

func usd(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }

// crossLeg is one side of a cross-currency transfer: an account, a day, and an amount in its own
// commodity, so a USD leg is a real USD amount rather than the CAD the plain line helper assumes.
func crossLeg(id, account string, day int, amount model.Amount, description string) model.Transaction {
	tx := lineIn(id, account, day, 0, description)
	tx.Amount = amount
	return tx
}

// A cross-currency transfer: a thousand Canadian dollars leaves chequing and seven hundred forty US
// dollars land in a USD account, seen once in each statement. The amounts are not equal and
// opposite, so the plain transfer fold cannot pair them. It is still one movement and must book
// once: the CAD leg, categorized to the USD account at the rate it cleared, carries the whole thing,
// and the USD sighting is the duplicate.
func TestACrossCurrencyTransferBooksOnce(t *testing.T) {
	log := newLog()
	importOne(t, log, crossLeg("c", "Assets:Chequing:CAD", 1, cad(-100000), "FX TO USD"))
	importOne(t, log, crossLeg("u", "Assets:USD", 2, usd(74000), "FX FROM CAD"))

	price := cad(100000)
	if err := books.Categorize(log, "human", "", "c", "", "Transfer to USD",
		[]model.Posting{{Account: "Assets:USD", Amount: usd(74000), Cost: &price}}); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	txs, entries := ledger(t, log)
	if len(txs) != 1 {
		t.Fatalf("got %d entries, want 1: the FX move was booked twice", len(txs))
	}
	if txs[0].ID != "c" {
		t.Fatalf("kept %q, want the earlier CAD leg", txs[0].ID)
	}
	if !entries[0].Balances(txs[0]) {
		t.Errorf("the kept entry does not balance: %+v", entries[0])
	}
}

// The cost tie is load-bearing. A USD deposit and an unrelated CAD withdrawal are not a transfer
// just because they are foreign to each other: only a posting that names the other account and ties
// the two real amounts together — the quantity received at the price paid — marks one movement.
func TestACrossCurrencyPairWithoutTheCostTieBooksBoth(t *testing.T) {
	log := newLog()
	importOne(t, log, crossLeg("c", "Assets:Chequing:CAD", 1, cad(-100000), "DINNER"))
	importOne(t, log, crossLeg("u", "Assets:USD", 2, usd(74000), "REFUND"))

	if err := books.Categorize(log, "human", "", "c", "", "Dinner", whole("Expenses:Food", -100000)); err != nil {
		t.Fatalf("Categorize: %v", err)
	}

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("got %d entries, want 2: without a cost tie these are two events", len(txs))
	}
}

// A match is a correction, so a later one supersedes: breaking a forced pair undoes it.
func TestALaterMatchSupersedesAnEarlierOne(t *testing.T) {
	log := newLog()
	importOne(t, log, line("a", 1, -50000, "MOVED OUT"))
	importOne(t, log, lineIn("b", "Assets:Bank:Savings", 2, 50000, "MOVED IN"))

	if err := books.Match(log, "human", "a", "b", true); err != nil {
		t.Fatalf("force: %v", err)
	}
	if err := books.Match(log, "human", "a", "", false); err != nil {
		t.Fatalf("break: %v", err)
	}
	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("the later break should undo the force, got %d", len(txs))
	}
}
