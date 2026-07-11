package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// This is the harness for working on the tool: a lengthy statement through the whole pipeline,
// over and over. CSV in (every messy amount shape banks emit, duplicate lines included), rules
// placing almost everything, a ledger-file statement joining it, the artifact rendered, and the
// artifact imported into a second, empty book with the same rules — which must fold to the same
// books: same fingerprints, same health line. That equality only holds because the artifact
// carries each line's raw description as a memo note; the rules and corrections themselves are
// deliberately NOT in the artifact — they live in the log, which is the backup.

// lengthyCSV writes fourMonths of statement lines: ten a month, every amount shape the parser
// accepts, and an identical pair on one day so the -1/-2 fingerprint suffixes are exercised.
func lengthyCSV(t *testing.T, path string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("Date,Description,Amount\n")
	for m := 1; m <= 4; m++ {
		fmt.Fprintf(&b, "2026-%02d-01,SHELL GAS #123,-62.40\n", m)
		fmt.Fprintf(&b, "2026-%02d-02,ACME HARDWARE #4471,(84.20)\n", m)
		fmt.Fprintf(&b, "2026-%02d-03,CITY WATER UTILITY,-118.75\n", m)
		fmt.Fprintf(&b, "2026-%02d-05,E-TRANSFER FROM J SMITH,1600.00\n", m)
		fmt.Fprintf(&b, "2026-%02d-06,WIDGETCO INVOICE 1042,\"$2,500.00\"\n", m)
		fmt.Fprintf(&b, "2026-%02d-07,COFFEE HOUSE 12,-5.00\n", m)
		fmt.Fprintf(&b, "2026-%02d-07,COFFEE HOUSE 12,-5.00\n", m) // identical, deliberately
		fmt.Fprintf(&b, "2026-%02d-09,POWER CO,-142.03\n", m)
		fmt.Fprintf(&b, "2026-%02d-10,MONTHLY FEE,-16.95\n", m)
		fmt.Fprintf(&b, "2026-%02d-12,UNKNOWN MERCHANT 88,-39.99\n", m)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("writing csv: %v", err)
	}
}

// visaLedger is a statement that arrives in ledger form: one line a rule knows, one it does not.
const visaLedger = `2026/02/10  * INTEREST CHARGE
  Expenses:Interest  10.00 CAD
  Liabilities:Card:Visa

2026/02/15  * MYSTERY CARD THING
  Expenses:Whatever  20.00 CAD
  Liabilities:Card:Visa
`

// theRules is the rule set both books get. Rules live in the log, not the artifact, so the second
// book authors them itself — that they then place the imported artifact identically is the point.
func theRules(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{
		{"shell", "-category", "Expenses:Travel:Fuel", "-payee", "Fuel Stop"},
		{"acme hardware", "-category", "Expenses:Materials", "-payee", "Acme Hardware"},
		{"city water", "-category", "Expenses:Utilities:Water"},
		{"e-transfer from j smith", "-category", "Income:Rent", "-payee", "J. Smith"},
		{"widgetco", "-category", "Income:Consulting", "-payee", "WidgetCo"},
		{"coffee house", "-category", "Expenses:Meals", "-payee", "Coffee House"},
		{"power co", "-category", "Expenses:Utilities:Power"},
		{"monthly fee", "-category", "Expenses:Bank Fees"},
		{"interest charge", "-category", "Expenses:Interest"},
	} {
		if err := ruleSetOne(args); err != nil {
			t.Fatalf("rules set %v: %v", args, err)
		}
	}
}

// foldStore folds the book in the current directory and summarizes it.
func foldStore(t *testing.T) ([]model.Transaction, []model.Entry, bookSummary) {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	txs, entries, err := books.Ledger(s.Log)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	sum, err := summarize(txs, entries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	return txs, entries, sum
}

func healthLine(t *testing.T, sum bookSummary) string {
	t.Helper()
	if len(sum.Totals) != 1 {
		t.Fatalf("want one commodity in the summary, got %+v", sum.Totals)
	}
	c := sum.Totals[0]
	return fmt.Sprintf("lines %d, uncategorized %d | income %s | expenses %s | net %s | unknown %s",
		sum.Lines, sum.UncategorizedLines, c.Income, c.Expenses, c.Net, c.Uncategorized)
}

func TestRoundTripLengthyBooks(t *testing.T) {
	// Book one: the CSV, the rules, and a ledger-form statement.
	bookHere(t)
	csv := filepath.Join(t.TempDir(), "statement.csv")
	lengthyCSV(t, csv)
	csvArgs := []string{csv, "-account", "Assets:Bank:Chequing", "-currency", "CAD", "-amount", "Amount"}
	if err := importCmd(csvArgs); err != nil {
		t.Fatalf("import csv: %v", err)
	}
	if err := importCmd(csvArgs); err != nil { // idempotence: a re-import is a no-op
		t.Fatalf("re-import csv: %v", err)
	}
	theRules(t)

	visa := filepath.Join(t.TempDir(), "visa.ledger")
	if err := os.WriteFile(visa, []byte(visaLedger), 0o644); err != nil {
		t.Fatalf("writing visa.ledger: %v", err)
	}
	if err := importCmd([]string{visa}); err != nil {
		t.Fatalf("import visa.ledger: %v", err)
	}

	txs1, _, sum1 := foldStore(t)
	want := "lines 42, uncategorized 5 | income 16400.00 CAD | expenses 1747.32 CAD | net 14652.68 CAD | unknown -179.96 CAD"
	if got := healthLine(t, sum1); got != want {
		t.Fatalf("book one folded to\n  %s\nwant\n  %s", got, want)
	}

	// The artifact: rendered twice, byte-identical, because the books are a pure function of the log.
	if err := renderBooks([]string{"-format", "ledger"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	artifact := s.Ledger()
	first, err := os.ReadFile(artifact)
	s.Close()
	if err != nil {
		t.Fatalf("reading artifact: %v", err)
	}
	if err := renderBooks([]string{"-format", "ledger"}); err != nil {
		t.Fatalf("re-render: %v", err)
	}
	second, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("re-reading artifact: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("two renders of the same log differ")
	}

	// Book two: empty, the same rules, and only the artifact as input. The memo notes carry each
	// line's raw description, so the fingerprints regenerate identically and the rules fire the same.
	bookHere(t)
	theRules(t)
	if err := importCmd([]string{artifact}); err != nil {
		t.Fatalf("import artifact: %v", err)
	}

	txs2, entries2, sum2 := foldStore(t)
	if got := healthLine(t, sum2); got != want {
		t.Fatalf("book two folded to\n  %s\nwant the same books\n  %s", got, want)
	}

	ids1 := map[string]bool{}
	for _, tx := range txs1 {
		ids1[tx.ID] = true
	}
	if len(txs2) != len(txs1) {
		t.Fatalf("book two has %d lines, book one %d", len(txs2), len(txs1))
	}
	for _, tx := range txs2 {
		if !ids1[tx.ID] {
			t.Errorf("fingerprint %s (%s) regenerated differently", tx.ID, tx.Description)
		}
	}

	// One concrete line, checked all the way through: the rule renamed the payee, the memo kept
	// the description, and both survived the trip.
	var fuel int
	for i, tx := range txs2 {
		if tx.Description != "SHELL GAS #123" {
			continue
		}
		fuel++
		if entries2[i].Payee != "Fuel Stop" || entries2[i].Postings[0].Account != "Expenses:Travel:Fuel" {
			t.Errorf("fuel line folded to %q / %v", entries2[i].Payee, entries2[i].Postings)
		}
	}
	if fuel != 4 {
		t.Errorf("want the 4 monthly fuel lines to keep their raw description, found %d", fuel)
	}

	// And the artifact is idempotent as an input, exactly as a statement is.
	if err := importCmd([]string{artifact}); err != nil {
		t.Fatalf("re-import artifact: %v", err)
	}
	txs3, _, _ := foldStore(t)
	if len(txs3) != len(txs2) {
		t.Fatalf("re-importing the artifact grew the books from %d to %d lines", len(txs2), len(txs3))
	}
}
