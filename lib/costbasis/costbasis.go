// Package costbasis folds a run of acquisitions and disposals into the cost each disposal consumes.
//
// Two policies are offered. Under the average cost base (ACB) every purchase blends into one
// running cost per holding, and a sale draws on that blended cost; this is the rule Canadian tax
// uses for capital property. Under first in, first out (FIFO) each purchase is a lot in a queue and
// a sale drains the oldest lots first; this is common elsewhere. Which one an account uses is a
// setting the caller supplies per account, so a book can hold both.
//
// Either way the base is a fold, not a stored number, so correcting an earlier purchase price moves
// every later sale's base and gain on the next regeneration. A full disposal returns exactly the
// cost that remains, so liquidating a position never rounds; a partial disposal rounds to the
// currency's minor unit, and the rounding cannot leak, because the cost consumed plus the cost left
// always equals the cost that went in.
package costbasis

import (
	"fmt"
	"math/big"

	"bkpr.pro/bkpr/lib/model"
)

// Policy is a cost-basis method: how a disposal's cost is drawn from what was acquired. The set is
// closed (ACB, FIFO), so a Policy cannot be implemented outside this package; a logged setting picks
// one by Name.
type Policy interface {
	Name() string
	newHolding() holding
}

// holding is one account's running cost under a policy. It is the unit the policies differ on: ACB
// keeps a single blended parcel, FIFO keeps a queue of them.
type holding interface {
	acquire(qty, cost model.Amount) error
	dispose(qty model.Amount) (model.Amount, error)
}

// ACB and FIFO are the two policies. They carry no state, so one value serves every account.
var (
	ACB  Policy = acb{}
	FIFO Policy = fifo{}
)

// ByName selects a policy by its logged name. An unknown name is an error rather than a silent
// default, so a typo in a setting is caught when it is recorded.
func ByName(name string) (Policy, error) {
	switch name {
	case ACB.Name():
		return ACB, nil
	case FIFO.Name():
		return FIFO, nil
	default:
		return nil, fmt.Errorf("costbasis: unknown policy %q; use %q or %q", name, ACB.Name(), FIFO.Name())
	}
}

// Fixed selects one policy for every account, the common single-jurisdiction book.
func Fixed(p Policy) func(string) Policy { return func(string) Policy { return p } }

// State is the running cost of every holding as acquisitions and disposals fold by in order. It is
// mutated in date order by the caller: acquire before dispose, so a sale always has a cost to draw
// on. Each account's holding is created under the policy the selector returns for it, so accounts
// on different policies coexist in one fold.
type State struct {
	policyFor func(account string) Policy
	holdings  map[string]holding
}

// New starts with no holdings, taking each account's policy from the selector.
func New(policyFor func(account string) Policy) *State {
	return &State{policyFor: policyFor, holdings: map[string]holding{}}
}

// Acquire adds a purchase to a holding: more units, and more total cost. The units carry the
// commodity being held (a share), and the cost carries the commodity paid (the cash). The first
// purchase in an account fixes its policy.
func (s *State) Acquire(account string, qty, cost model.Amount) error {
	h := s.holdings[account]
	if h == nil {
		h = s.policyFor(account).newHolding()
		s.holdings[account] = h
	}
	if err := h.acquire(qty, cost); err != nil {
		return fmt.Errorf("costbasis: %s: %w", account, err)
	}
	return nil
}

// Dispose removes qty units from a holding and reports the cost they carried under its policy.
func (s *State) Dispose(account string, qty model.Amount) (model.Amount, error) {
	h := s.holdings[account]
	if h == nil {
		return model.Amount{}, fmt.Errorf("costbasis: %s holds nothing to dispose of", account)
	}
	basis, err := h.dispose(qty)
	if err != nil {
		return model.Amount{}, fmt.Errorf("costbasis: %s: %w", account, err)
	}
	return basis, nil
}

// acb blends every purchase into one running cost.
type acb struct{}

func (acb) Name() string        { return "acb" }
func (acb) newHolding() holding { return &blended{} }

// blended is the ACB holding: one parcel, its cost the running total.
type blended struct {
	held   parcel
	seeded bool
}

func (b *blended) acquire(qty, cost model.Amount) error {
	if !b.seeded {
		b.held, b.seeded = parcel{qty: qty, cost: cost}, true
		return nil
	}
	return b.held.grow(qty, cost)
}

func (b *blended) dispose(qty model.Amount) (model.Amount, error) {
	if !b.seeded {
		return model.Amount{}, fmt.Errorf("holds nothing to dispose of")
	}
	return b.held.take(qty)
}

// fifo keeps each purchase as its own lot and drains the oldest first.
type fifo struct{}

func (fifo) Name() string        { return "fifo" }
func (fifo) newHolding() holding { return &queue{} }

// queue is the FIFO holding: purchases in the order they were made.
type queue struct {
	lots []parcel
}

func (q *queue) acquire(qty, cost model.Amount) error {
	q.lots = append(q.lots, parcel{qty: qty, cost: cost})
	return nil
}

func (q *queue) dispose(qty model.Amount) (model.Amount, error) {
	if len(q.lots) == 0 {
		return model.Amount{}, fmt.Errorf("holds nothing to dispose of")
	}
	basis := model.Amount{Scale: q.lots[0].cost.Scale, Commodity: q.lots[0].cost.Commodity}
	remaining := qty

	for len(q.lots) > 0 && remaining.Units > 0 {
		lot := &q.lots[0]
		after, err := remaining.Add(lot.qty.Negate())
		if err != nil {
			return model.Amount{}, err
		}
		if after.Units >= 0 {
			// The oldest lot is wholly consumed: take its exact cost and move to the next.
			if basis, err = basis.Add(lot.cost); err != nil {
				return model.Amount{}, err
			}
			remaining = after
			q.lots = q.lots[1:]
			continue
		}
		// The oldest lot outlasts the sale: take a rounded fraction and leave the rest in place.
		part, err := lot.take(remaining)
		if err != nil {
			return model.Amount{}, err
		}
		if basis, err = basis.Add(part); err != nil {
			return model.Amount{}, err
		}
		remaining = model.Amount{Scale: remaining.Scale, Commodity: remaining.Commodity}
	}

	if remaining.Units > 0 {
		return model.Amount{}, fmt.Errorf("cannot dispose of more than is held")
	}
	return basis, nil
}

// parcel is a quantity held at a total cost, the shared unit both policies keep.
type parcel struct {
	qty  model.Amount
	cost model.Amount
}

// grow folds another purchase into the parcel, blending the quantities and the costs.
func (p *parcel) grow(qty, cost model.Amount) error {
	nextQty, err := p.qty.Add(qty)
	if err != nil {
		return fmt.Errorf("holds %s, cannot add %s", p.qty, qty)
	}
	nextCost, err := p.cost.Add(cost)
	if err != nil {
		return fmt.Errorf("cost is %s, cannot add %s", p.cost, cost)
	}
	p.qty, p.cost = nextQty, nextCost
	return nil
}

// take removes qty from the parcel and returns the cost it carried: the whole remaining cost when
// the parcel empties, otherwise its cost times the fraction sold, rounded to the minor unit.
func (p *parcel) take(qty model.Amount) (model.Amount, error) {
	if p.qty.Units <= 0 {
		return model.Amount{}, fmt.Errorf("holds nothing to dispose of")
	}
	remaining, err := p.qty.Add(qty.Negate())
	if err != nil {
		return model.Amount{}, err
	}
	if remaining.Units < 0 {
		return model.Amount{}, fmt.Errorf("holds %s, cannot dispose of %s", p.qty, qty)
	}

	basis := p.cost
	if remaining.Units != 0 {
		basis = model.Amount{
			Units:     mulDivRound(p.cost.Units, qty, p.qty),
			Scale:     p.cost.Scale,
			Commodity: p.cost.Commodity,
		}
	}

	leftCost, err := p.cost.Add(basis.Negate())
	if err != nil {
		return model.Amount{}, err
	}
	p.qty, p.cost = remaining, leftCost
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
