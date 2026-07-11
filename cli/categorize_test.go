package main

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/model"
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
