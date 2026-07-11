package source_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/model"
)

func readLedger(t *testing.T, text string) []model.Transaction {
	t.Helper()
	txs, err := source.ReadLedger(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	return txs
}

// The statement line is reconstructed from the entry: the account it came from is the single posting
// with no amount, and the line's amount is the negation of what the categorized postings account
// for, exactly as the writer elided it.
func TestReadLedgerReconstructsAnExpenseLine(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * Acme Hardware
  Expenses:Materials  84.20 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 1 {
		t.Fatalf("got %d transactions, want 1", len(txs))
	}
	tx := txs[0]
	if tx.Account != "Assets:Bank:Chequing" {
		t.Errorf("account = %q, want the amountless posting", tx.Account)
	}
	if tx.Amount.String() != "-84.20 CAD" {
		t.Errorf("amount = %q, want -84.20 CAD (the negated categorized side)", tx.Amount)
	}
	if tx.Date.Format("2006-01-02") != "2026-03-01" || tx.Description != "Acme Hardware" {
		t.Errorf("date/desc = %q / %q", tx.Date.Format("2006-01-02"), tx.Description)
	}
	if tx.ID == "" {
		t.Error("no fingerprint")
	}
}

// A deposit: the income posting is negative, so the reconstructed statement line is positive.
func TestReadLedgerReconstructsADeposit(t *testing.T) {
	txs := readLedger(t, `2026/03/03  * Rent
  Income:Rent  -168.00 CAD
  Assets:Bank:Chequing
`)
	if txs[0].Amount.String() != "168.00 CAD" {
		t.Errorf("amount = %q, want 168.00 CAD", txs[0].Amount)
	}
}

// A split entry sums its categorized postings before negating.
func TestReadLedgerSumsASplit(t *testing.T) {
	txs := readLedger(t, `2026/03/05  * Hardware
  Expenses:A  10.00 CAD
  Expenses:B  15.00 CAD
  Assets:Bank:Chequing
`)
	if txs[0].Amount.String() != "-25.00 CAD" {
		t.Errorf("amount = %q, want -25.00 CAD", txs[0].Amount)
	}
}

// Entries are separated by a blank line, and comments are ignored.
func TestReadLedgerReadsSeveralEntriesAndSkipsComments(t *testing.T) {
	txs := readLedger(t, `; opening balance stuff
2026/03/01  * One
  Expenses:A  1.00 CAD
  Assets:Bank:Chequing

2026/03/02  * Two
  Expenses:B  2.00 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 2 || txs[0].Description != "One" || txs[1].Description != "Two" {
		t.Fatalf("got %+v, want two entries One and Two", txs)
	}
}

// Two identical entries are two lines, so their fingerprints are made distinct rather than colliding.
func TestReadLedgerKeepsIdenticalEntriesDistinct(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * Coffee
  Expenses:Meals  5.00 CAD
  Assets:Bank:Chequing

2026/03/01  * Coffee
  Expenses:Meals  5.00 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 2 || txs[0].ID == txs[1].ID {
		t.Fatalf("ids = %q, %q; want two distinct", txs[0].ID, txs[1].ID)
	}
}

// Without exactly one amountless posting, the account the statement came from cannot be told, so
// the file is refused rather than guessed.
func TestReadLedgerRefusesAnEntryWithNoElidedAccount(t *testing.T) {
	_, err := source.ReadLedger(strings.NewReader(`2026/03/01  * X
  Expenses:A  10.00 CAD
  Expenses:B  -10.00 CAD
`))
	if err == nil {
		t.Fatal("an entry with every posting priced has no statement account; want an error")
	}
}

func TestReadLedgerRefusesTwoElidedAccounts(t *testing.T) {
	_, err := source.ReadLedger(strings.NewReader(`2026/03/01  * X
  Expenses:A  10.00 CAD
  Assets:Bank:Chequing
  Assets:Bank:Savings
`))
	if err == nil {
		t.Fatal("two amountless postings are ambiguous; want an error")
	}
}
