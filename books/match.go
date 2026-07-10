package books

import (
	"time"

	"github.com/dallasread/bookkeeper/model"
)

// transferDays is how far apart the two sightings of one movement may be dated. A transfer often
// clears the sending and receiving accounts a day or two apart.
const transferDays = 5

// suppressed returns the transactions that are the duplicate sighting of an internal transfer, and
// so must not book a second entry.
//
// A transfer between two accounts you own appears once in each account's statement. Each sighting,
// categorized to the other account, is a complete and balanced entry on its own, so booking both
// moves the money out and then back and nets it to zero, losing the movement. One sighting is kept
// and the other suppressed.
//
// This is a pure fold: no event records a pairing, because the pairing is recomputable from the
// lines and their categorization. Two sightings pair when each names the other's account, their
// amounts are equal and opposite, and their dates are within the window. The earlier sighting is
// kept, which dates the entry at the movement's origin and makes the choice deterministic.
//
// Pairing is greedy in date order. When several sightings of the same size sit in one window they
// are interchangeable, so any valid pairing suppresses the same number, and the count of real
// movements always survives.
func suppressed(txs []model.Transaction, entries []model.Entry) map[string]bool {
	dup := map[string]bool{}
	consumed := make([]bool, len(txs))

	for i := range txs {
		if consumed[i] {
			continue
		}
		for j := i + 1; j < len(txs); j++ {
			if consumed[j] {
				continue
			}
			if isTransferPair(txs[i], entries[i], txs[j], entries[j]) {
				dup[txs[j].ID] = true // txs are date-ordered, so j is the later sighting
				consumed[i], consumed[j] = true, true
				break
			}
		}
	}
	return dup
}

// isTransferPair reports whether two sightings are the same internal movement. The mutual naming is
// the load-bearing check: without it, a real expense and a coincidental deposit of the same size
// would be mistaken for a transfer and one of them would vanish.
func isTransferPair(a model.Transaction, ea model.Entry, b model.Transaction, eb model.Entry) bool {
	if a.Account == b.Account {
		return false
	}
	if !a.Amount.Equal(b.Amount.Negate()) {
		return false
	}
	if daysApart(a.Date, b.Date) > transferDays {
		return false
	}
	return postsTo(ea, b.Account) && postsTo(eb, a.Account)
}

func postsTo(e model.Entry, account string) bool {
	for _, p := range e.Postings {
		if p.Account == account {
			return true
		}
	}
	return false
}

func daysApart(x, y time.Time) int {
	d := x.Sub(y)
	if d < 0 {
		d = -d
	}
	return int(d.Hours()) / 24
}
