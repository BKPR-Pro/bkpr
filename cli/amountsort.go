package main

import (
	"math"
	"sort"

	"github.com/dallasread/bkpr/lib/model"
)

// amountMagnitude is an amount's signed decimal value as a float, the key for sorting by amount. It
// orders amounts of one commodity exactly; across commodities it compares the raw numbers, so to order
// a mixed book by true value, restate it into one commodity first (books -value). Money at these
// magnitudes is exact in a float64.
func amountMagnitude(a model.Amount) float64 {
	if a.Scale == 0 {
		return float64(a.Units)
	}
	return float64(a.Units) / math.Pow(10, float64(a.Scale))
}

// balanceMagnitude sums an account's per-commodity balances into one sort key. A single-commodity
// account (the common case) gets its exact value; a mixed account sums its raw figures.
func balanceMagnitude(per map[string]model.Amount) float64 {
	var v float64
	for _, a := range per {
		v += amountMagnitude(a)
	}
	return v
}

// orderAccountsByAmount returns the names ordered by their balance magnitude, ascending unless desc,
// ties broken by name so the order is stable and deterministic. The input is not mutated.
func orderAccountsByAmount(names []string, balances map[string]map[string]model.Amount, desc bool) []string {
	out := append([]string(nil), names...)
	sort.SliceStable(out, func(i, j int) bool {
		vi, vj := balanceMagnitude(balances[out[i]]), balanceMagnitude(balances[out[j]])
		if vi == vj {
			return out[i] < out[j]
		}
		if desc {
			return vi > vj
		}
		return vi < vj
	})
	return out
}

// orderLinesByAmount reorders the parallel txs/entries by each line's amount magnitude, keeping every
// transaction paired with its entry, ascending unless desc. Ties keep their incoming (date) order.
// New slices are returned; the inputs are not mutated.
func orderLinesByAmount(txs []model.Transaction, entries []model.Entry, desc bool) ([]model.Transaction, []model.Entry) {
	idx := make([]int, len(txs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		va, vb := amountMagnitude(txs[idx[a]].Amount), amountMagnitude(txs[idx[b]].Amount)
		if desc {
			return va > vb
		}
		return va < vb
	})
	st := make([]model.Transaction, len(txs))
	se := make([]model.Entry, len(entries))
	for k, i := range idx {
		st[k], se[k] = txs[i], entries[i]
	}
	return st, se
}
