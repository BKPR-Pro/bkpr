package books

import (
	"encoding/json"
	"fmt"

	"github.com/dallasread/bkpr/lib/costbasis"
	"github.com/dallasread/bkpr/lib/eventlog"
	"github.com/dallasread/bkpr/lib/model"
)

// Sell asserts a disposal: which shares left an account, and where the realized gain lands. Only the
// intent is recorded. The cost base the shares carry is folded from the account's purchases when the
// books are built, so the gain is derived and a corrected purchase price moves it. The disposal is
// resolved once here first, so an impossible sale (more shares than are held) is refused when it is
// asserted rather than surfacing later at render time.
func Sell(log *eventlog.Log, actor, why, txID, payee, gainAccount string, disposals []model.Posting) error {
	if gainAccount == "" {
		return fmt.Errorf("books: a sale needs an account for its gain")
	}
	if len(disposals) == 0 {
		return fmt.Errorf("books: a sale needs at least one holding to dispose of")
	}

	// The asserted posting names a positive quantity sold; a disposal reduces the holding, so it is
	// carried as a negative share amount with no price, the price being the cost base found on the fold.
	postings := make([]model.Posting, len(disposals))
	for i, d := range disposals {
		if d.Amount.Units <= 0 {
			return fmt.Errorf("books: sell %s: a disposal quantity must be positive", d.Account)
		}
		postings[i] = model.Posting{Account: d.Account, Amount: d.Amount.Negate()}
	}

	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}
	txID = tx.ID // the caller may have quoted a prefix; the assertion keys to the line

	// Resolve the sale against the current books to prove it is possible before recording it.
	if err := validateSale(log, txID, model.Entry{Payee: payee, Postings: postings, Gain: gainAccount}); err != nil {
		return err
	}

	// A sale's legs are spelled, never derived from a category, so no tax overlay may restate them.
	data, err := json.Marshal(categorizedData{Payee: payee, Postings: postings, Gain: gainAccount, Why: why, ExplicitPosts: true})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionCategorized,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// validateSale folds the current log, substitutes the pending sale for its line, and resolves the
// disposals under the book's cost-basis policies. It surfaces an oversell or a mixed-commodity
// mistake now, at the moment the sale is asserted, the same way an unbalanced categorization is
// refused before it reaches the log.
func validateSale(log *eventlog.Log, txID string, pending model.Entry) error {
	txs, entries, err := categorized(log)
	if err != nil {
		return err
	}
	policies, err := Policies(log)
	if err != nil {
		return err
	}
	target := -1
	for i, tx := range txs {
		if tx.ID == txID {
			// Resolution prices the disposals in place, so validate against a copy: the postings the
			// caller records must stay unpriced, or the base would freeze here instead of folding on read.
			entries[i] = model.Entry{Payee: pending.Payee, Postings: clonePostings(pending.Postings), Gain: pending.Gain}
			target = i
		}
	}
	if target < 0 {
		return fmt.Errorf("books: no transaction %q to sell against", txID)
	}
	if err := resolveDisposals(txs, entries, policies.For); err != nil {
		return err
	}
	if !entries[target].Balances(txs[target]) {
		return fmt.Errorf("books: the sale does not account for %s", txs[target].Amount.Negate())
	}
	return nil
}

// clonePostings copies the slice and each posting so a resolution that prices them in place cannot
// reach back into the caller's data.
func clonePostings(ps []model.Posting) []model.Posting {
	out := make([]model.Posting, len(ps))
	copy(out, ps)
	return out
}

// resolveDisposals walks the entries in date order and, for each one that names a gain account,
// prices the shares leaving at their cost base and appends the gain. Purchases accumulate the base
// as they pass, so a sale draws on everything bought before it. The entries are mutated in place.
//
// Buy and sell are told apart by sign: a priced share posting with a positive quantity is a
// purchase that adds to the base, and an unpriced share posting on a gain-bearing entry is the
// disposal to value. The gain is whatever is left between the base and the proceeds, so the entry
// ends up accounting for the whole line. Each account's base is drawn under the policy the selector
// returns for it, so ACB and FIFO accounts fold side by side.
func resolveDisposals(txs []model.Transaction, entries []model.Entry, policyFor func(string) costbasis.Policy) error {
	state := costbasis.New(policyFor)

	for i := range entries {
		tx, e := txs[i], &entries[i]

		for _, p := range e.Postings {
			if p.Cost != nil && p.Amount.Commodity != tx.Amount.Commodity && p.Amount.Units > 0 {
				cost := *p.Cost
				if cost.Units < 0 {
					cost = cost.Negate()
				}
				if err := state.Acquire(p.Account, p.Amount, cost); err != nil {
					return err
				}
			}
		}

		if e.Gain == "" {
			continue
		}

		for j := range e.Postings {
			p := &e.Postings[j]
			if p.Cost != nil || p.Amount.Commodity == tx.Amount.Commodity || p.Amount.Units >= 0 {
				continue
			}
			basis, err := state.Dispose(p.Account, p.Amount.Negate())
			if err != nil {
				return fmt.Errorf("%s %s: %w", tx.Date.Format("2006/01/02"), e.Payee, err)
			}
			p.Cost = &basis
		}

		gain, err := e.Shortfall(tx)
		if err != nil {
			return err
		}
		e.Postings = append(e.Postings, model.Posting{Account: e.Gain, Amount: gain})
	}
	return nil
}
