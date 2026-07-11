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

// importAs imports a line under a given actor, so a test can stand in for a live-source pull
// ("source:rent") versus a bank statement ("statement:march").
func importAs(t *testing.T, log *eventlog.Log, actor string, tx model.Transaction) {
	t.Helper()
	if _, err := books.Import(log, actor, []model.Transaction{tx}); err != nil {
		t.Fatalf("Import: %v", err)
	}
}

// The reason this slice exists. The rent app records a payment, and the same money lands in the
// bank statement; pulling both and booking both would count the rent twice. They are recognised as
// one deposit and booked once, keeping the pulled record because it knows the lease.
func TestPulledRentAndItsBankDepositBookOnce(t *testing.T) {
	log := newLog()
	importAs(t, log, "source:rent", line("rent:a1", 3, 168000, "Rent for March 2026"))
	importAs(t, log, "statement:march", line("bankfp", 3, 168000, "E-TRANSFER FROM TENANT"))

	txs, _ := ledger(t, log)
	if len(txs) != 1 {
		t.Fatalf("got %d entries, want 1: the rent was booked twice", len(txs))
	}
	if txs[0].ID != "rent:a1" {
		t.Errorf("kept %q, want the pulled rent record (it knows the lease)", txs[0].ID)
	}
}

// Only the rent app has it (the bank statement has not been imported yet), so it books on its own.
func TestPulledRentBooksWithoutABankMatch(t *testing.T) {
	log := newLog()
	importAs(t, log, "source:rent", line("rent:a1", 3, 168000, "Rent for March 2026"))

	if txs, _ := ledger(t, log); len(txs) != 1 {
		t.Fatalf("got %d entries, want 1", len(txs))
	}
}

// Suppression needs one live-sourced side. A bank deposit that coincidentally equals a pulled
// payment but in a different account is not the same deposit, so both book.
func TestADepositInAnotherAccountIsNotTheSameMoney(t *testing.T) {
	log := newLog()
	importAs(t, log, "source:rent", line("rent:a1", 3, 168000, "Rent for March 2026"))
	importAs(t, log, "statement:savings", lineIn("bankfp", "Assets:Bank:Savings", 3, 168000, "DEPOSIT"))

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("got %d entries, want 2: different accounts are different money", len(txs))
	}
}

// Two bank lines of the same size, neither pulled, are not deduped against each other: that would
// be the re-import problem, which is handled by the fingerprint, not by this matching.
func TestTwoBankLinesAreNotDedupedAgainstEachOther(t *testing.T) {
	log := newLog()
	importAs(t, log, "statement:march", line("a", 3, 168000, "DEPOSIT ONE"))
	importAs(t, log, "statement:march", line("b", 3, 168000, "DEPOSIT TWO"))

	if txs, _ := ledger(t, log); len(txs) != 2 {
		t.Fatalf("got %d entries, want 2: two real bank deposits", len(txs))
	}
}
