package books_test

import (
	"testing"

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

// Only one leg imported (the other account's statement has not been pulled) books normally; there
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
