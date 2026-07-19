package main

import (
	"regexp"
	"testing"

	"github.com/dallasread/bkpr/lib/model"
)

// regLine pairs a statement line with its categorized entry, the two sides the register reads.
func regLine(id string, day int, account string, amount model.Amount, payee string, postings ...model.Posting) (model.Transaction, model.Entry) {
	return model.Transaction{ID: id, Account: account, Date: on(day), Amount: amount},
		model.Entry{Payee: payee, Postings: postings}
}

// The unfiltered register is every account's statement interleaved: each line with its amount, the
// account it moved, and the door it entered through. No balance runs, because a balance across
// different accounts is not a number.
func TestRegisterListsEveryLineWithItsDoor(t *testing.T) {
	tx1, e1 := regLine("aaa", 1, "Assets:Bank:Chequing", cad2(-6240), "Shell", post("Expenses:Fuel", cad2(6240)))
	tx2, e2 := regLine("bbb", 2, "Liabilities:PC Mastercard", cad2(-8420), "Acme", post("Expenses:Repairs", cad2(8420)))
	doors := map[string]string{"aaa": "connector:rbc", "bbb": "statement:hand.txt"}

	rows, err := buildRegister([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, doors, nil)
	if err != nil {
		t.Fatalf("buildRegister: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Door != "connector:rbc" || rows[1].Door != "statement:hand.txt" {
		t.Errorf("doors = %q, %q; want each line's importing door", rows[0].Door, rows[1].Door)
	}
	if rows[0].Amount.String() != "-62.40 CAD" {
		t.Errorf("amount = %q, want the statement line's own amount", rows[0].Amount)
	}
	if rows[0].Account != "Assets:Bank:Chequing" || rows[1].Account != "Liabilities:PC Mastercard" {
		t.Errorf("accounts = %q, %q; want the account each line moved", rows[0].Account, rows[1].Account)
	}
	if rows[0].Payee != "Shell" {
		t.Errorf("payee = %q, want the entry's payee", rows[0].Payee)
	}
}

// Filtered to one account, the register is that account's bank statement: only the lines that moved
// it, each row's amount the movement on that account, with a balance running down the column.
func TestRegisterAccountFilterRunsABalance(t *testing.T) {
	tx1, e1 := regLine("aaa", 1, "Assets:Bank:Chequing", cad2(160000), "J Smith", post("Income:Rent", cad2(-160000)))
	tx2, e2 := regLine("bbb", 2, "Liabilities:PC Mastercard", cad2(-8420), "Acme", post("Expenses:Repairs", cad2(8420)))
	tx3, e3 := regLine("ccc", 3, "Assets:Bank:Chequing", cad2(-6240), "Shell", post("Expenses:Fuel", cad2(6240)))

	rows, err := buildRegister(
		[]model.Transaction{tx1, tx2, tx3}, []model.Entry{e1, e2, e3},
		map[string]string{}, regexp.MustCompile("(?i)Chequing"))
	if err != nil {
		t.Fatalf("buildRegister: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want only the two chequing lines", len(rows))
	}
	if rows[0].Balance.String() != "1600.00 CAD" {
		t.Errorf("balance after the deposit = %q, want 1600.00 CAD", rows[0].Balance)
	}
	if rows[1].Balance.String() != "1537.60 CAD" {
		t.Errorf("running balance = %q, want 1537.60 CAD", rows[1].Balance)
	}
}

// A folded transfer is one line, but each account still sees its own side: the card's register reads
// the posting leg that landed on it, signed the way the card's statement would print it.
func TestRegisterAccountBalanceCountsAPostingLeg(t *testing.T) {
	tx, e := regLine("aaa", 5, "Assets:Bank:Chequing", cad2(-100000), "Payment",
		post("Liabilities:PC Mastercard", cad2(100000)))

	rows, err := buildRegister([]model.Transaction{tx}, []model.Entry{e},
		map[string]string{}, regexp.MustCompile("(?i)PC Mastercard"))
	if err != nil {
		t.Fatalf("buildRegister: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the transfer seen from the card's side", len(rows))
	}
	if rows[0].Amount.String() != "1000.00 CAD" {
		t.Errorf("amount = %q, want the paydown as the card's statement prints it", rows[0].Amount)
	}
	if rows[0].Account != "Liabilities:PC Mastercard" {
		t.Errorf("account = %q, want the matched posting's account", rows[0].Account)
	}
}

// A routed leg (categorize -source) moves the sub-account, not the account the line was imported
// on, so the register must read the entry's source account the way the fold books it.
func TestRegisterAccountBalanceFollowsARoutedSourceLeg(t *testing.T) {
	tx, e := regLine("aaa", 5, "Liabilities:PC Mastercard", cad2(-8420), "Acme",
		post("Expenses:Repairs", cad2(8420)))
	e.Source = "Liabilities:PC Mastercard:9 Schoodic"

	rows, err := buildRegister([]model.Transaction{tx}, []model.Entry{e},
		map[string]string{}, regexp.MustCompile("(?i)9 Schoodic"))
	if err != nil {
		t.Fatalf("buildRegister: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the routed child to carry the movement", len(rows))
	}
	if rows[0].Account != "Liabilities:PC Mastercard:9 Schoodic" {
		t.Errorf("account = %q, want the routed source account", rows[0].Account)
	}
}

// Candidate twins are the same date and the same amount entering through different doors: the
// double-entered purchase fingerprints cannot catch. One door twice is not a candidate -- a bank
// legitimately charges the same coffee twice in a day -- and its own fingerprints already dedupe.
func TestTwinGroupsPairSameDateAndAmountAcrossDoors(t *testing.T) {
	tx1, e1 := regLine("aaa", 4, "Liabilities:PC Mastercard", cad2(-8420), "Acme", post("Expenses:Repairs", cad2(8420)))
	tx2, e2 := regLine("bbb", 4, "Liabilities:PC Mastercard", cad2(-8420), "Acme Hardware", post("Expenses:Repairs", cad2(8420)))
	tx3, e3 := regLine("ccc", 4, "Assets:Bank:Chequing", cad2(-6240), "Shell", post("Expenses:Fuel", cad2(6240)))
	doors := map[string]string{"aaa": "connector:pc", "bbb": "statement:hand.txt", "ccc": "connector:rbc"}

	groups := twinGroups([]model.Transaction{tx1, tx2, tx3}, []model.Entry{e1, e2, e3}, doors, nil)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want the one cross-door collision", len(groups))
	}
	g := groups[0]
	if len(g.Rows) != 2 || g.Rows[0].ID != "aaa" || g.Rows[1].ID != "bbb" {
		t.Errorf("group rows = %+v, want the two twins", g.Rows)
	}
	if g.Rows[0].Door == g.Rows[1].Door {
		t.Errorf("both rows carry door %q, want the two doors named", g.Rows[0].Door)
	}
	if g.Rows[0].PostsTo != "Expenses:Repairs" {
		t.Errorf("posts to = %q, want each twin's categorization shown", g.Rows[0].PostsTo)
	}
}

func TestTwinGroupsIgnoreSameDoorCollisions(t *testing.T) {
	tx1, e1 := regLine("aaa", 4, "Liabilities:PC Mastercard", cad2(-450), "Coffee")
	tx2, e2 := regLine("bbb", 4, "Liabilities:PC Mastercard", cad2(-450), "Coffee")
	doors := map[string]string{"aaa": "connector:pc", "bbb": "connector:pc"}

	if groups := twinGroups([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, doors, nil); len(groups) != 0 {
		t.Errorf("got %d groups, want none: two coffees through one door are two coffees", len(groups))
	}
}

// The same value written with a different number of decimal places is the same amount, so 84.2 in a
// hand ledger and 84.20 off the connector must still collide.
func TestTwinGroupsMatchAcrossScales(t *testing.T) {
	tx1, e1 := regLine("aaa", 4, "Liabilities:PC Mastercard", model.Amount{Units: -842, Scale: 1, Commodity: "CAD"}, "Acme")
	tx2, e2 := regLine("bbb", 4, "Liabilities:PC Mastercard", cad2(-8420), "Acme")
	doors := map[string]string{"aaa": "statement:hand.txt", "bbb": "connector:pc"}

	if groups := twinGroups([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, doors, nil); len(groups) != 1 {
		t.Fatalf("got %d groups, want 84.2 and 84.20 to collide", len(groups))
	}
}

func TestTwinGroupsKeepCommoditiesApart(t *testing.T) {
	tx1, e1 := regLine("aaa", 4, "Assets:Bank:Chequing", cad2(-8420), "Acme")
	tx2, e2 := regLine("bbb", 4, "Assets:Bank:USD", model.Amount{Units: -8420, Scale: 2, Commodity: "USD"}, "Acme")
	doors := map[string]string{"aaa": "connector:rbc", "bbb": "statement:hand.txt"}

	if groups := twinGroups([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, doors, nil); len(groups) != 0 {
		t.Errorf("got %d groups, want none: 84.20 CAD is not 84.20 USD", len(groups))
	}
}

// On the accrual basis an open invoice booked its income already, so a deposit categorized straight
// to income on the same date and amount is that income counted twice -- a candidate twin.
func TestTwinGroupsFlagAnOpenAccrualAgainstACashDouble(t *testing.T) {
	dep, de := regLine("dep", 4, "Assets:Bank:Chequing", cad2(160000), "J Smith", post("Income:Rent", cad2(-160000)))
	acc, ae := regLine("inv-fold-id", 4, "Assets:Receivable", cad2(160000), "J Smith", post("Income:Rent", cad2(-160000)))
	doors := map[string]string{"dep": "connector:rbc", "inv-fold-id": "invoice"}

	groups := twinGroups([]model.Transaction{dep, acc}, []model.Entry{de, ae}, doors, nil)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want the open invoice flagged against the deposit", len(groups))
	}
}

// A settled accrual and the bank line that paid it are one recorded flow: the books already link
// them, so the register must not offer the pair back as a suspected double every month.
func TestTwinGroupsSkipASettledAccrualAndItsPayment(t *testing.T) {
	dep, de := regLine("dep", 4, "Assets:Bank:Chequing", cad2(160000), "J Smith", post("Assets:Receivable", cad2(-160000)))
	acc, ae := regLine("inv-fold-id", 4, "Assets:Receivable", cad2(160000), "J Smith", post("Income:Rent", cad2(-160000)))
	doors := map[string]string{"dep": "connector:rbc", "inv-fold-id": "invoice"}
	settlements := map[string]string{"inv-fold-id": "dep"}

	groups := twinGroups([]model.Transaction{dep, acc}, []model.Entry{de, ae}, doors, settlements)
	if len(groups) != 0 {
		t.Errorf("got %d groups, want none: the settlement already pairs these lines", len(groups))
	}
}
