package main

import (
	"testing"

	"bkpr.pro/bkpr/lib/model"
)

func usd(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }

// -category takes the whole line to one account, in the line's own commodity with the sign flipped.
func TestPostingsForCategoryTakesTheWholeLine(t *testing.T) {
	post, err := postingsFor("Expenses:Repairs", nil, usd(-8420))
	if err != nil {
		t.Fatalf("postingsFor: %v", err)
	}
	if len(post) != 1 || post[0].Account != "Expenses:Repairs" || post[0].Amount.String() != "84.20 USD" {
		t.Errorf("post = %+v", post)
	}
}

// A bare -post amount keeps the line's commodity, so an ordinary split is unchanged.
func TestPostingsForSplitKeepsTheLineCommodity(t *testing.T) {
	split := splitFlag{{account: "Expenses:A", quantity: "40.00"}, {account: "Expenses:B", quantity: "44.20"}}
	post, err := postingsFor("", split, usd(-8420))
	if err != nil {
		t.Fatalf("postingsFor: %v", err)
	}
	if len(post) != 2 || post[0].Amount.String() != "40.00 USD" || post[1].Cost != nil {
		t.Errorf("post = %+v", post)
	}
}

// A share bought with cash carries its own commodity and an @@ total price, which reaches the
// posting as a Cost so the entry can balance across two commodities.
func TestPostingsForReadsAPricedShare(t *testing.T) {
	split := splitFlag{{account: "Assets:Brokerage:AAPL", quantity: "10 AAPL @@ 1000.00 USD"}}
	post, err := postingsFor("", split, usd(-100000))
	if err != nil {
		t.Fatalf("postingsFor: %v", err)
	}
	if len(post) != 1 || post[0].Amount.String() != "10 AAPL" {
		t.Fatalf("post = %+v", post)
	}
	if post[0].Cost == nil || post[0].Cost.String() != "1000.00 USD" {
		t.Errorf("cost = %v, want 1000.00 USD", post[0].Cost)
	}
	if !(model.Entry{Postings: post}).Balances(model.Transaction{Amount: usd(-100000)}) {
		t.Error("the priced posting should account for the cash line")
	}
}

// -tax-rate on categorize does exactly what the rule form does: the pre-tax amount to the category
// (net = total / (1 + rate)) and the extracted tax to the tax account, so the hand form and the rule
// form of the same decision cannot diverge.
func TestTaxedPostingsSplitTheTaxOutOfTheTotal(t *testing.T) {
	post, err := taxedPostings("Expenses:Repairs:Materials", "15%", "Assets:HST ITC", usd(-11500))
	if err != nil {
		t.Fatalf("taxedPostings: %v", err)
	}
	if len(post) != 2 {
		t.Fatalf("want two postings, got %+v", post)
	}
	if post[0].Account != "Expenses:Repairs:Materials" || post[0].Amount.String() != "100.00 USD" {
		t.Errorf("net = %s %s, want the category at 100.00 USD", post[0].Account, post[0].Amount)
	}
	if post[1].Account != "Assets:HST ITC" || post[1].Amount.String() != "15.00 USD" {
		t.Errorf("tax = %s %s, want the tax account at 15.00 USD", post[1].Account, post[1].Amount)
	}
}

// However the rate rounds, the two legs must still account for the whole line, so a hand-computed
// split can never drift a cent from the bank amount.
func TestTaxedPostingsBalanceOnAnUnevenRate(t *testing.T) {
	line := usd(-8420) // 13% does not divide evenly
	post, err := taxedPostings("Expenses:Repairs", "13%", "Assets:HST ITC", line)
	if err != nil {
		t.Fatalf("taxedPostings: %v", err)
	}
	if !(model.Entry{Postings: post}).Balances(model.Transaction{Amount: line}) {
		t.Errorf("the taxed split does not account for the line: %+v", post)
	}
}

// A rate written as a bare decimal would silently mean 0.15%, so it is refused, exactly as a rule
// refuses it.
func TestTaxedPostingsRejectABareDecimalRate(t *testing.T) {
	if _, err := taxedPostings("Expenses:Repairs", "0.15", "Assets:HST ITC", usd(-11500)); err == nil {
		t.Fatal("a rate without a percent sign should be refused")
	}
}

// A -sell names a positive quantity of shares with its commodity, and produces a disposal posting
// for that account. The base is not restated, so it carries no price.
func TestDisposalsForReadsASale(t *testing.T) {
	post, err := disposalsFor(splitFlag{{account: "Assets:Brokerage:AAPL", quantity: "10 AAPL"}})
	if err != nil {
		t.Fatalf("disposalsFor: %v", err)
	}
	if len(post) != 1 || post[0].Account != "Assets:Brokerage:AAPL" || post[0].Amount.String() != "10 AAPL" {
		t.Errorf("post = %+v", post)
	}
	if post[0].Cost != nil {
		t.Errorf("a sale carries no price, got %v", post[0].Cost)
	}
}

// A sale takes no price: its cost base is folded, not restated, so an @@ on a -sell is refused
// rather than quietly ignored.
func TestDisposalsForRejectsAPriceOnASale(t *testing.T) {
	if _, err := disposalsFor(splitFlag{{account: "Assets:Brokerage:AAPL", quantity: "10 AAPL @@ 1000.00 USD"}}); err == nil {
		t.Fatal("a sale should not accept a price")
	}
}
