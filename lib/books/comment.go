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
// matches no posting, or more than one, is refused rather than guessed. Commenting a line the rules
// categorized freezes their current answer for that one line, the same way a hand correction does,
// because a note is an assertion about a specific line and cannot be carried by a rule.
func Comment(log *eventlog.Log, actor, why, txID, account, text string, remove bool) error {
	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}

	entry, err := entryFor(log, tx.ID)
	if err != nil {
		return err
	}

	idx, err := postingToComment(entry.Postings, account)
	if err != nil {
		return err
	}
	if remove {
		text = ""
	}
	entry.Postings[idx].Comment = text

	data, err := json.Marshal(categorizedData{
		Payee: entry.Payee, Postings: entry.Postings, Gain: entry.Gain, Why: why,
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
// it or a later assertion did. It is the stored form, before disposals are priced, so re-asserting
// it records the same fact the books already hold.
func entryFor(log *eventlog.Log, txID string) (model.Entry, error) {
	txs, entries, err := categorized(log)
	if err != nil {
		return model.Entry{}, err
	}
	for i, tx := range txs {
		if tx.ID == txID {
			return entries[i], nil
		}
	}
	return model.Entry{}, fmt.Errorf("books: no transaction %q to comment on", txID)
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
