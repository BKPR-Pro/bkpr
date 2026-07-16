package source

import (
	"testing"
)

// A personal RBC Cash Back Mastercard lives in the personal section of RBC's modern online banking,
// so it renders on the same Angular `rbc-transaction-list-transaction-new` grid as the business
// Current Account -- not the legacy site, and not the Visa's cc-date-large date cell. Being a card it
// is a liability, so RBC signs a charge as a positive Withdrawal and a credit (a payment, a cash-back
// reward) as a negative Deposit, the reverse of a chequing. This pins that whole path end to end
// against a captured-shape page: the modern grid is read with the liability sign flip, a charge lands
// negative and a credit positive in the books, and the owing balance is carried from the running
// Balance column (shown positive; the CLI negates it for a liability).
func TestRBCReadsACashBackMastercard(t *testing.T) {
	requireBrowserTests(t)
	url := serveFixture(t, "rbc_mastercard.html")

	// A liability account, so the script flips the card's signs.
	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: url, DefaultCurrency: "CAD",
		Account: "Liabilities:Personal:RBC Cash Back Mastercard",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the Mastercard: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:RBC Cash Back Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}

	// The desktop grid holds four transactions; the responsive `-mob` duplicate must not be counted.
	want := []struct {
		date, desc, amount string
	}{
		// A cash-back reward is a credit -> positive (reduces what is owed).
		{"2026-07-12", "CASH BACK REWARD", "15.00 CAD"},
		// A purchase is a charge -> negative (increases what is owed).
		{"2026-07-11", "Contactless purchase TEST GROCERY", "-50.00 CAD"},
		// A payment is a credit -> positive.
		{"2026-07-05", "PAYMENT - THANK YOU / PAIEMENT - MERCI", "500.00 CAD"},
		// The annual fee is a charge -> negative.
		{"2026-06-30", "ANNUAL FEE", "-120.00 CAD"},
	}
	if len(res.Transactions) != len(want) {
		t.Fatalf("got %d transactions, want %d: %q", len(res.Transactions), len(want), out)
	}
	for i, w := range want {
		tx := res.Transactions[i]
		if got := tx.Date.Format("2006-01-02"); got != w.date {
			t.Errorf("row %d date = %s, want %s", i, got, w.date)
		}
		if tx.Description != w.desc {
			t.Errorf("row %d description = %q, want %q", i, tx.Description, w.desc)
		}
		if tx.Amount.String() != w.amount {
			t.Errorf("row %d amount = %s, want %s", i, tx.Amount.String(), w.amount)
		}
		if tx.ID == "" {
			t.Errorf("row %d was not fingerprinted", i)
		}
	}

	// The newest row's running balance is the amount owing, shown positive; the CLI negates it for a
	// liability at import time.
	if !res.HasBalance || res.Balance.String() != "484.66 CAD" {
		t.Errorf("balance = %s (has=%v), want 484.66 CAD", res.Balance, res.HasBalance)
	}
}
