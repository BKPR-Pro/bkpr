package books

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bkpr.pro/bkpr/lib/eventlog"
	"bkpr.pro/bkpr/lib/model"
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
// kept, which dates the entry at the movement's origin and makes the choice deterministic. Two
// sightings on one day have no origin, so there the side with the most recent assertion is kept
// -- the categorization someone made speaks for the movement, and between two assertions the
// later wins, as a later fact does everywhere in the log (see keptOf).
//
// Pairing is greedy in date order. When several sightings of the same size sit in one window they
// are interchangeable, so any valid pairing suppresses the same number, and the count of real
// movements always survives.
//
// Beyond mutual naming, two sightings also pair when they are simply the same amount moving the other
// way between two accounts you own, within the window, and neither side has been given a real income
// or expense identity (both are Uncategorized, or already point at an owned account). This is the
// hands-off case: an ordinary transfer the rules never named still pairs by itself. The "neither is
// claimed" guard is load-bearing -- without it a categorized grocery bill and a categorized paycheck
// of the same size would be mistaken for a transfer and one would vanish. When such a pair is found
// the kept (earlier) leg is booked as the transfer to the other account, so both accounts' balances
// stay right even though neither statement named the other. A wrong pairing is undone with `match`.
//
// The loose fold is a guess made where the rules stayed silent, so it yields to an assertion: two
// lines both explicitly categorized -- corrected by hand, or carried from a ledger file that names
// its own accounts -- are not fused, because each assertion says what its line is. Hand-kept books
// write every movement once, so there a loose pairing can only be a coincidence of size; the named
// fold, which has real evidence, still applies. A genuine transfer the rules missed on one asserted
// side is recovered with `match`.
func suppressed(txs []model.Transaction, entries []model.Entry, overrides map[string]matchedData, owned map[string]bool, assertedAt map[string]int) map[string]bool {
	dup := map[string]bool{}
	consumed := make([]bool, len(txs))
	pos := make(map[string]int, len(txs))
	for i, tx := range txs {
		pos[tx.ID] = i
	}

	// A broken match books on its own, so it is pre-consumed: the automatic pairing leaves it alone
	// and it is never suppressed.
	for i, tx := range txs {
		if o, ok := overrides[tx.ID]; ok && !o.Paired {
			consumed[i] = true
		}
	}

	// A forced match asserts two sightings are one movement the automatic fold could not see (it pairs
	// only on mutual naming). The losing sighting is suppressed.
	for i, tx := range txs {
		o, ok := overrides[tx.ID]
		if !ok || !o.Paired || consumed[i] {
			continue
		}
		j, ok := pos[o.With]
		if !ok || consumed[j] {
			continue
		}
		_, lost := keptOf(txs, assertedAt, i, j)
		dup[txs[lost].ID] = true
		consumed[i], consumed[j] = true, true
	}

	// Internal transfers: one movement seen in two accounts you own. The losing sighting is
	// suppressed.
	for i := range txs {
		if consumed[i] {
			continue
		}
		for j := i + 1; j < len(txs); j++ {
			if consumed[j] {
				continue
			}
			named := isTransferPair(txs[i], entries[i], txs[j], entries[j]) ||
				isCrossTransferPair(txs[i], entries[i], txs[j], entries[j])
			loose := !named && isOwnedTransferPair(txs[i], entries[i], txs[j], entries[j], owned)
			if loose && bothAsserted(txs[i], txs[j], assertedAt) {
				// Two assertions, not two unnamed sightings: trust what each line says rather than
				// guessing them into one movement on size alone.
				loose = false
			}
			if !named && !loose {
				continue
			}
			lost := j
			if named {
				_, lost = keptOf(txs, assertedAt, i, j)
			}
			if loose {
				// Neither statement named the other, so book the kept leg as the transfer: a single
				// posting of the movement to the other account, so both accounts' balances are right.
				entries[i] = model.Entry{Postings: []model.Posting{
					{Account: txs[j].Account, Amount: txs[i].Amount.Negate()},
				}}
			}
			dup[txs[lost].ID] = true
			consumed[i], consumed[j] = true, true
			break
		}
	}
	return dup
}

// keptOf chooses which sighting of a pair survives the fold. Dated apart, the earlier is kept:
// the entry belongs at the movement's origin. On one day there is no origin, and the choice must
// not hang on the accident of fingerprint order, so the side with the most recent assertion is
// kept -- the categorization someone made speaks for the movement, and between two assertions
// the later wins, as a later fact does everywhere in the log. With nothing asserted the earlier
// index is kept, so the choice stays deterministic.
func keptOf(txs []model.Transaction, assertedAt map[string]int, i, j int) (kept, lost int) {
	kept, lost = i, j
	if j < i { // txs are date-ordered, so the lower index is the earlier sighting
		kept, lost = j, i
	}
	if !txs[i].Date.Equal(txs[j].Date) {
		return kept, lost
	}
	ri, ok := assertedAt[txs[i].ID]
	if !ok {
		ri = -1
	}
	rj, ok := assertedAt[txs[j].ID]
	if !ok {
		rj = -1
	}
	switch {
	case ri > rj:
		return i, j
	case rj > ri:
		return j, i
	}
	return kept, lost
}

// bothAsserted reports whether both sightings carry an explicit categorization -- a hand correction
// or a fact carried from a ledger file -- rather than a rules guess. Two assertions are statements of
// what each line is, so the loose fold leaves them alone.
func bothAsserted(a, b model.Transaction, assertedAt map[string]int) bool {
	_, ai := assertedAt[a.ID]
	_, bi := assertedAt[b.ID]
	return ai && bi
}

// isOwnedTransferPair reports whether two sightings are the same movement between two accounts you own,
// recognized without either statement naming the other. Both are some transaction's source account, so
// both are owned; the checks that remain are equal-and-opposite, within the window, and -- the guard
// that keeps a real expense and a coincidental deposit apart -- that neither leg carries a real income
// or expense category. A leg that is Uncategorized, or already points only at accounts you own, is
// fair game; a leg the rules placed in Expenses or Income is not.
func isOwnedTransferPair(a model.Transaction, ea model.Entry, b model.Transaction, eb model.Entry, owned map[string]bool) bool {
	if a.Account == b.Account {
		return false
	}
	if !a.Amount.Equal(b.Amount.Negate()) {
		return false
	}
	if daysApart(a.Date, b.Date) > transferDays {
		return false
	}
	return !claimed(ea, owned) && !claimed(eb, owned)
}

// claimed reports whether an entry has been given a real spending identity: a posting to an account
// that is neither Uncategorized nor one you own. Such an entry is a categorized expense or deposit,
// not an unclaimed line free to be read as one side of a transfer.
func claimed(e model.Entry, owned map[string]bool) bool {
	for _, p := range e.Postings {
		if p.Account == model.Uncategorized || strings.HasSuffix(p.Account, ":"+model.Uncategorized) {
			continue
		}
		if owned[p.Account] {
			continue
		}
		return true
	}
	return false
}

// ActionMatched records a manual override of the automatic transfer fold: a pairing the fold could
// not see, or a break of one it wrongly made. It is keyed by a transaction's fingerprint, and a
// later one supersedes, because it is a correction.
const ActionMatched = "matched"

type matchedData struct {
	With   string `json:"with,omitempty"` // the other sighting, when forcing a pair
	Paired bool   `json:"paired"`         // true forces a pairing, false breaks one
}

// Match records that a transaction is, or is not, the duplicate sighting of a transfer. With paired
// true and a partner it forces the pair, so the later sighting is suppressed; with paired false it
// breaks any pairing, so the line books on its own. A later Match on the same line supersedes.
func Match(log *eventlog.Log, actor, txID, withID string, paired bool) error {
	if txID == "" {
		return fmt.Errorf("books: a match needs a transaction")
	}
	if paired && withID == "" {
		return fmt.Errorf("books: forcing a match needs the other transaction")
	}
	// Both lines must exist, so a typo is refused rather than recorded against nothing, and a
	// quoted prefix resolves to the full fingerprint before it is written.
	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}
	txID = tx.ID
	if withID != "" {
		with, err := Transaction(log, withID)
		if err != nil {
			return err
		}
		withID = with.ID
	}
	data, err := json.Marshal(matchedData{With: withID, Paired: paired})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionMatched,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// matches folds the log into the current override per transaction; a later event replaces an earlier.
func matches(log *eventlog.Log) (map[string]matchedData, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string]matchedData{}
	for _, e := range events {
		if e.Collection != CollectionTransaction || e.Action != ActionMatched {
			continue
		}
		var d matchedData
		if err := e.Decode(&d); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out[e.RecordID] = d
	}
	return out, nil
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

// isCrossTransferPair reports whether two sightings in different commodities are the same movement.
// A move between two of your own accounts across a currency boundary shows a thousand dollars leaving
// one and seven hundred forty landing in the other: not equal and opposite, so isTransferPair cannot
// see it. The evidence is instead the price. One leg, categorized, names the other's account, the
// quantity that landed there, and the cost it took, which is exactly this leg's own amount. That cost
// tie stands in for the mutual naming a same-commodity pair relies on: it ties the two real amounts
// to each other, so a coincidental foreign deposit is not mistaken for the far side of a transfer.
func isCrossTransferPair(a model.Transaction, ea model.Entry, b model.Transaction, eb model.Entry) bool {
	if a.Account == b.Account || a.Amount.Commodity == b.Amount.Commodity {
		return false
	}
	if daysApart(a.Date, b.Date) > transferDays {
		return false
	}
	return crossTies(ea, a, b) || crossTies(eb, b, a)
}

// crossTies reports whether self's entry books the far leg of a transfer to other: a posting into
// other's account for the amount that landed there, priced at the magnitude that left self. The price
// is what proves the two lines are one movement rather than two of the same size.
func crossTies(selfEntry model.Entry, self, other model.Transaction) bool {
	for _, p := range selfEntry.Postings {
		if p.Account != other.Account || p.Cost == nil {
			continue
		}
		if p.Amount.Equal(other.Amount) && magnitude(*p.Cost).Equal(magnitude(self.Amount)) {
			return true
		}
	}
	return false
}

// magnitude drops an amount's sign, so a cost stored as a positive total compares equal to the
// negative amount that left an account.
func magnitude(a model.Amount) model.Amount {
	if a.Units < 0 {
		return a.Negate()
	}
	return a
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
