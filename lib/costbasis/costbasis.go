// Package costbasis folds a run of acquisitions and disposals into the cost each disposal consumes.
//
// The default policy is the average cost base (ACB): every purchase blends into one running cost
// per holding, and a sale draws on that blended cost rather than the price of any single lot. This
// is the rule Canadian tax uses for capital property, and it is the reason the cost base is a fold
// rather than a stored number: correct an earlier purchase price and every later sale's basis, and
// so its gain, moves with it on the next regeneration.
//
// A full disposal returns exactly the cost that remains, so liquidating a position never rounds. A
// partial disposal rounds to the currency's minor unit, and the rounding cannot leak: the cost
// consumed plus the cost left always equals the cost that went in.
package costbasis

import (
	"fmt"
	"math/big"

	"github.com/dallasread/bookkeeper/lib/model"
)

// State is the running cost of every holding as acquisitions and disposals fold by in order. It is
// mutated in date order by the caller: acquire before dispose, so a sale always has a cost to draw
// on.
type State struct {
	holdings map[string]*holding
}

type holding struct {
	qty  model.Amount // how many units are held
	cost model.Amount // what they cost in total, in the book's commodity
}

// New starts with no holdings.
func New() *State { return &State{holdings: map[string]*holding{}} }

// Acquire adds a purchase to a holding: more units, and more total cost. The units carry the
// commodity being held (a share), and the cost carries the commodity paid (the cash).
func (s *State) Acquire(account string, qty, cost model.Amount) error {
	h := s.holdings[account]
	if h == nil {
		s.holdings[account] = &holding{qty: qty, cost: cost}
		return nil
	}
	nextQty, err := h.qty.Add(qty)
	if err != nil {
		return fmt.Errorf("costbasis: %s holds %s, cannot add %s", account, h.qty, qty)
	}
	nextCost, err := h.cost.Add(cost)
	if err != nil {
		return fmt.Errorf("costbasis: %s cost is %s, cannot add %s", account, h.cost, cost)
	}
	h.qty, h.cost = nextQty, nextCost
	return nil
}

// Dispose removes qty units from a holding and reports the cost they carried under ACB: the total
// cost times the fraction of the holding sold. Selling the whole holding returns the whole cost, so
// the last disposal absorbs whatever rounding the partial ones left behind.
func (s *State) Dispose(account string, qty model.Amount) (model.Amount, error) {
	h := s.holdings[account]
	if h == nil || h.qty.Units <= 0 {
		return model.Amount{}, fmt.Errorf("costbasis: %s holds nothing to dispose of", account)
	}

	remaining, err := h.qty.Add(qty.Negate())
	if err != nil {
		return model.Amount{}, err
	}
	if remaining.Units < 0 {
		return model.Amount{}, fmt.Errorf("costbasis: %s holds %s, cannot dispose of %s", account, h.qty, qty)
	}

	var basis model.Amount
	if remaining.Units == 0 {
		basis = h.cost // a full disposal returns the exact remaining cost, no rounding
	} else {
		basis = model.Amount{
			Units:     mulDivRound(h.cost.Units, qty, h.qty),
			Scale:     h.cost.Scale,
			Commodity: h.cost.Commodity,
		}
	}

	leftCost, err := h.cost.Add(basis.Negate())
	if err != nil {
		return model.Amount{}, err
	}
	h.qty, h.cost = remaining, leftCost
	return basis, nil
}

// mulDivRound returns round(cost * sold / held) to the nearest minor unit, in big integers so a
// large holding cannot overflow. The quantities share a commodity, so their raw units are
// restated at a common scale before the ratio is taken and the scale cancels.
func mulDivRound(cost int64, sold, held model.Amount) int64 {
	scale := sold.Scale
	if held.Scale > scale {
		scale = held.Scale
	}
	soldUnits := restate(sold, scale)
	heldUnits := restate(held, scale)

	num := new(big.Int).Mul(big.NewInt(cost), big.NewInt(soldUnits))
	den := big.NewInt(heldUnits)

	// Round half up: (num + den/2) / den, on magnitudes so the sign is not lost.
	half := new(big.Int).Rsh(den, 1)
	num.Add(num, half)
	return new(big.Int).Quo(num, den).Int64()
}

// restate returns an amount's units at a finer scale, so two quantities of one commodity can be
// compared as plain integers.
func restate(a model.Amount, scale uint8) int64 {
	p := int64(1)
	for i := a.Scale; i < scale; i++ {
		p *= 10
	}
	return a.Units * p
}
