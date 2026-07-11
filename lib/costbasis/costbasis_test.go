package costbasis_test

import (
	"testing"

	"github.com/dallasread/bookkeeper/lib/costbasis"
	"github.com/dallasread/bookkeeper/lib/model"
)

func usd(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }
func aapl(n int64) model.Amount    { return model.Amount{Units: n, Scale: 0, Commodity: "AAPL"} }

// The whole of a single lot leaves at exactly what it cost: a full disposal never rounds.
func TestDisposingAWholeLotReturnsItsCost(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.ACB))
	if err := s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000)); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	basis, err := s.Dispose("Assets:Brokerage:AAPL", aapl(10))
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if basis.String() != "1000.00 USD" {
		t.Errorf("basis = %q, want the whole cost", basis.String())
	}
}

// Two lots at different prices average into one cost base, which is the point of ACB: a later sale
// draws on the blended cost, not the price of any one purchase.
func TestDisposalDrawsOnTheAverageCost(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.ACB))
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000)) // 10 @ 100
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(140000)) // 10 @ 140; ACB is 120

	basis, err := s.Dispose("Assets:Brokerage:AAPL", aapl(5))
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if basis.String() != "600.00 USD" { // 5 @ the 120 average
		t.Errorf("basis = %q, want 600.00 USD", basis.String())
	}

	// The rest of the holding keeps the same average, so liquidating it returns the exact remainder.
	rest, err := s.Dispose("Assets:Brokerage:AAPL", aapl(15))
	if err != nil {
		t.Fatalf("Dispose rest: %v", err)
	}
	if rest.String() != "1800.00 USD" {
		t.Errorf("remaining basis = %q, want 1800.00 USD", rest.String())
	}
}

// A partial disposal that does not divide evenly rounds to the cent, and the rounding does not
// leak: the cost consumed plus the cost left equals the cost that went in.
func TestPartialDisposalRoundsWithoutLeaking(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.ACB))
	s.Acquire("Assets:Brokerage:AAPL", aapl(3), usd(100000)) // $1000 over 3 shares

	one, _ := s.Dispose("Assets:Brokerage:AAPL", aapl(1)) // 1000/3 = 333.33 -> 333.33
	two, _ := s.Dispose("Assets:Brokerage:AAPL", aapl(2)) // the exact remainder

	total, _ := one.Add(two)
	if total.String() != "1000.00 USD" {
		t.Errorf("consumed basis = %q, want the whole 1000.00 USD back", total.String())
	}
}

// You cannot dispose of more than you hold: that is not a rounding question but a broken book, so
// it is an error rather than a negative holding.
func TestDisposingMoreThanHeldIsRefused(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.ACB))
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000))

	if _, err := s.Dispose("Assets:Brokerage:AAPL", aapl(11)); err == nil {
		t.Fatal("disposed more shares than were held")
	}
}

// Disposing from an account that holds nothing is likewise refused: there is no basis to draw on.
func TestDisposingFromAnEmptyHoldingIsRefused(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.ACB))
	if _, err := s.Dispose("Assets:Brokerage:AAPL", aapl(1)); err == nil {
		t.Fatal("disposed from an account that never held the commodity")
	}
}

// Holdings are per account, so a sale from one position never draws on another's cost base.
func TestHoldingsAreKeptPerAccount(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.ACB))
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000))
	s.Acquire("Assets:Brokerage:MSFT", model.Amount{Units: 10, Commodity: "MSFT"}, usd(300000))

	basis, err := s.Dispose("Assets:Brokerage:AAPL", aapl(10))
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if basis.String() != "1000.00 USD" {
		t.Errorf("basis = %q; a sale drew on the wrong account", basis.String())
	}
}

// FIFO draws on the oldest lot first, not a blended average, so the same two purchases give a
// different base than ACB: the first sale carries the price of the first lot.
func TestFIFODisposesTheOldestLotFirst(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.FIFO))
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000)) // lot 1: 10 @ 100
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(140000)) // lot 2: 10 @ 140

	basis, err := s.Dispose("Assets:Brokerage:AAPL", aapl(5))
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if basis.String() != "500.00 USD" { // 5 from lot 1 at 100, not the 120 average
		t.Errorf("basis = %q, want 500.00 USD (oldest lot)", basis.String())
	}
}

// A disposal that spans a lot boundary drains the oldest lot, then takes the rest from the next: 10
// sold is the last 5 of lot 1 plus the first 5 of lot 2.
func TestFIFOSpansLotsInOrder(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.FIFO))
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000)) // 10 @ 100
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(140000)) // 10 @ 140
	s.Dispose("Assets:Brokerage:AAPL", aapl(5))               // takes 5 @ 100

	basis, err := s.Dispose("Assets:Brokerage:AAPL", aapl(10)) // 5 @ 100 + 5 @ 140
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if basis.String() != "1200.00 USD" {
		t.Errorf("basis = %q, want 1200.00 USD (500 + 700)", basis.String())
	}
}

// Liquidating everything returns the exact total cost, however the lots were consumed, so FIFO
// rounding never leaks any more than ACB's does.
func TestFIFOFullLiquidationIsExact(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.FIFO))
	s.Acquire("Assets:Brokerage:AAPL", aapl(3), usd(100000)) // 1000/3 does not divide
	s.Acquire("Assets:Brokerage:AAPL", aapl(3), usd(50000))

	one, _ := s.Dispose("Assets:Brokerage:AAPL", aapl(1)) // partial within lot 1, rounds
	rest, _ := s.Dispose("Assets:Brokerage:AAPL", aapl(5))

	total, _ := one.Add(rest)
	if total.String() != "1500.00 USD" {
		t.Errorf("consumed basis = %q, want the whole 1500.00 USD", total.String())
	}
}

// FIFO refuses an oversell the same as ACB: you cannot dispose of more lots than you hold.
func TestFIFOOversellIsRefused(t *testing.T) {
	s := costbasis.New(costbasis.Fixed(costbasis.FIFO))
	s.Acquire("Assets:Brokerage:AAPL", aapl(10), usd(100000))
	if _, err := s.Dispose("Assets:Brokerage:AAPL", aapl(11)); err == nil {
		t.Fatal("disposed more shares than were held under FIFO")
	}
}

// The two policies are addressable by name, which is how a logged setting selects one; an unknown
// name is an error rather than a silent default.
func TestByName(t *testing.T) {
	if p, err := costbasis.ByName("acb"); err != nil || p.Name() != "acb" {
		t.Errorf("acb = %v, %v", p, err)
	}
	if p, err := costbasis.ByName("fifo"); err != nil || p.Name() != "fifo" {
		t.Errorf("fifo = %v, %v", p, err)
	}
	if _, err := costbasis.ByName("lifo"); err == nil {
		t.Error("an unknown policy name should be refused")
	}
}
