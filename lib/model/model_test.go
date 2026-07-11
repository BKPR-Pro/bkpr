package model_test

import (
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/model"
)

func usd(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }

func shares(n int64, symbol string) model.Amount {
	return model.Amount{Units: n, Scale: 0, Commodity: symbol}
}

func line(cents int64) model.Transaction {
	return model.Transaction{
		Account: "Assets:Brokerage:Cash",
		Date:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Amount:  usd(cents),
	}
}

// A cash line and a share posting are two commodities. Without a price they cannot be summed, so
// the entry does not balance: this is the boundary the priced posting opens.
func TestASharePostingWithoutAPriceDoesNotBalance(t *testing.T) {
	tx := line(-100000) // spent $1000 of cash
	e := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Brokerage:AAPL", Amount: shares(10, "AAPL")},
	}}

	if e.Balances(tx) {
		t.Fatal("an unpriced cross-commodity posting must not balance a cash line")
	}
}

// Buying 10 AAPL for $1000 of cash: the shares are worth their total cost, which offsets the cash
// that left. The price is what lets one entry hold two commodities.
func TestAPricedBuyBalancesTheCashItCost(t *testing.T) {
	tx := line(-100000) // $1000 out of the brokerage cash
	cost := usd(100000)
	e := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Brokerage:AAPL", Amount: shares(10, "AAPL"), Cost: &cost},
	}}

	if !e.Balances(tx) {
		t.Fatal("10 AAPL @@ 1000.00 USD should account for a -1000.00 USD cash line")
	}
}

// Selling is the mirror: the shares leave (a negative quantity) and their total is what came in as
// cash. The sign of the contribution follows the quantity, so a disposal offsets a positive line.
func TestAPricedSellBalancesTheCashItRaised(t *testing.T) {
	tx := line(120000) // $1200 into cash
	proceeds := usd(120000)
	e := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Brokerage:AAPL", Amount: shares(-10, "AAPL"), Cost: &proceeds},
	}}

	if !e.Balances(tx) {
		t.Fatal("-10 AAPL @@ 1200.00 USD should account for a +1200.00 USD cash line")
	}
}

// A trade can carry a cash fee in the same line: a priced share posting and a plain cash posting
// sum together, because both resolve to the statement's commodity.
func TestAPricedPostingSumsWithAPlainCashPosting(t *testing.T) {
	tx := line(-100500) // $1000 for shares plus a $5 fee
	cost := usd(100000)
	e := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Brokerage:AAPL", Amount: shares(10, "AAPL"), Cost: &cost},
		{Account: "Expenses:Brokerage:Fees", Amount: usd(500)},
	}}

	if !e.Balances(tx) {
		t.Fatal("a priced buy and a cash fee should together account for the whole line")
	}
}

// The price must be in the statement's commodity, or it cannot be summed against the line. A price
// in the wrong currency is refused rather than guessed through a second conversion.
func TestAPriceInTheWrongCommodityDoesNotBalance(t *testing.T) {
	tx := line(-100000)
	cost := model.Amount{Units: 137000, Scale: 2, Commodity: "CAD"} // price given in CAD, line is USD
	e := model.Entry{Postings: []model.Posting{
		{Account: "Assets:Brokerage:AAPL", Amount: shares(10, "AAPL"), Cost: &cost},
	}}

	if e.Balances(tx) {
		t.Fatal("a price in a different commodity than the line must not balance")
	}
}
