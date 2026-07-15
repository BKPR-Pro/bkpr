package books

import (
	"encoding/json"
	"fmt"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

// Comment records a free-text note on one posting of a line, or clears it when remove is set. The
// note is commentary, not data: it takes no part in balancing, so the entry it rides on is
// re-asserted whole with only the one leg's comment changed, and never re-validated for balance.
//
// The account names which leg the note belongs to. It may be omitted only when the entry has a
// single posting, the one place a note could go; a split must name the leg, and an account that
// matches no posting, or more than one, is refused rather than guessed. Naming the line's own
// account reaches the source (elided) leg -- the balancing posting the ledger infers, which is not
// among the categorized postings -- and records the note against the transaction instead, its own
// latest-wins fact. Commenting a line the rules categorized freezes their current answer for that
// one line, the same way a hand correction does, because a note is an assertion about a specific
// line and cannot be carried by a rule.
func Comment(log *eventlog.Log, actor, why, txID, account, text string, remove bool) error {
	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}

	entry, explicitPosts, err := entryFor(log, tx.ID)
	if err != nil {
		return err
	}

	if remove {
		text = ""
	}

	// The source (elided) leg is the line's own account, not one of the categorized postings, so a note
	// on it is recorded against the transaction rather than the entry. It cannot unbalance the line, so
	// like a categorized note it needs no balance re-check.
	onSource, err := namesSourceLeg(tx, entry.Postings, account)
	if err != nil {
		return err
	}
	if onSource {
		return commentSource(log, actor, why, tx.ID, text)
	}

	idx, err := postingToComment(entry.Postings, account)
	if err != nil {
		return err
	}
	entry.Postings[idx].Comment = text

	// The freeze re-asserts the entry as it stands, and it must not soften it: legs the caller spelled
	// stay spelled, so a note never opens a deliberate no-split to a later tax overlay.
	data, err := json.Marshal(categorizedData{
		Payee: entry.Payee, Postings: entry.Postings, Gain: entry.Gain,
		BlockComments: entry.BlockComments, Why: why, ExplicitPosts: explicitPosts,
	})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: tx.ID, Action: ActionCategorized,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// entryFor folds the log into the categorization one line currently carries, whether the rules gave
// it or a later assertion did, and whether that assertion's legs were spelled by the caller. It is
// the stored form, before disposals are priced, so re-asserting it records the same fact the books
// already hold.
func entryFor(log *eventlog.Log, txID string) (model.Entry, bool, error) {
	txs, entries, err := categorized(log)
	if err != nil {
		return model.Entry{}, false, err
	}
	asserted, err := assertions(log)
	if err != nil {
		return model.Entry{}, false, err
	}
	for i, tx := range txs {
		if tx.ID == txID {
			return entries[i], asserted[txID].explicitPosts, nil
		}
	}
	return model.Entry{}, false, fmt.Errorf("books: no transaction %q to comment on", txID)
}

// namesSourceLeg reports whether the account names the transaction's own (elided, source) leg, the
// balancing posting the ledger infers and that is never among the categorized postings. It is named
// only when the account matches the line's account exactly; an empty account names the sole
// categorized posting instead, never the source leg. When that same account is also a categorized
// posting -- a line posted back to its own account -- the note could belong to either leg, so it is
// refused as ambiguous rather than guessed.
func namesSourceLeg(tx model.Transaction, postings []model.Posting, account string) (bool, error) {
	if account == "" || account != tx.Account {
		return false, nil
	}
	for _, p := range postings {
		if p.Account == account {
			return false, fmt.Errorf("books: account %q is both the source leg and a categorized posting; the note is ambiguous", account)
		}
	}
	return true, nil
}

// postingToComment finds the one leg a note belongs to. With no account named it is the sole
// posting, and a split is refused as ambiguous. With an account named it must match exactly one
// posting: none is a typo, several is ambiguous, and both are refused rather than guessed.
func postingToComment(postings []model.Posting, account string) (int, error) {
	if account == "" {
		if len(postings) != 1 {
			return 0, fmt.Errorf("books: this entry has %d postings; name one with an account", len(postings))
		}
		return 0, nil
	}
	idx := -1
	for i, p := range postings {
		if p.Account != account {
			continue
		}
		if idx >= 0 {
			return 0, fmt.Errorf("books: account %q names more than one posting; the note is ambiguous", account)
		}
		idx = i
	}
	if idx < 0 {
		return 0, fmt.Errorf("books: no posting on account %q to comment on", account)
	}
	return idx, nil
}
