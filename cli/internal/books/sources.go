package books

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
	"github.com/dallasread/bookkeeper/cli/internal/source"
)

// CollectionSource is keyed by the ledger account, which is a source's identity. A source says how
// to read one account's statements.
//
// A source is a door, not a fold input. transaction.imported stores its line already normalized, so
// fixing a source changes what the next import reads and nothing about what past imports produced.
// Fixing a rule, by contrast, reclassifies history. Sources are logged for provenance: a mapping
// that drifts in a file changes how lines normalize, which changes their fingerprints, which
// silently books a second rent payment.
const CollectionSource = "source"

// sourceData carries everything but the account, which is the record id and would only invite the
// two to disagree.
type sourceData struct {
	Currency    string `json:"currency"`
	Date        string `json:"date"`
	Description string `json:"description"`
	DateFormat  string `json:"date_format"`
	Amount      string `json:"amount,omitempty"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Why         string `json:"why,omitempty"`
}

func (d sourceData) into(account string) source.CSV {
	return source.CSV{
		Account: account, Currency: d.Currency, Date: d.Date,
		Description: d.Description, DateFormat: d.DateFormat,
		Amount: d.Amount, Debit: d.Debit, Credit: d.Credit,
	}
}

func fromSource(s source.CSV) sourceData {
	return sourceData{
		Currency: s.Currency, Date: s.Date, Description: s.Description,
		DateFormat: s.DateFormat, Amount: s.Amount, Debit: s.Debit, Credit: s.Credit,
	}
}

// Sources folds the log into the accounts bookkeeper knows how to read, ordered by account.
func Sources(log *eventlog.Log) ([]source.CSV, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	set := map[string]source.CSV{}
	for _, e := range events {
		if e.Collection != CollectionSource {
			continue
		}
		var data sourceData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}

		switch e.Action {
		case ActionAdded, ActionChanged:
			set[e.RecordID] = data.into(e.RecordID)
		case ActionRemoved:
			delete(set, e.RecordID)
		}
	}

	out := make([]source.CSV, 0, len(set))
	for _, s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out, nil
}

// Source finds one account's source, or says which accounts it does know.
func Source(log *eventlog.Log, account string) (source.CSV, error) {
	set, err := Sources(log)
	if err != nil {
		return source.CSV{}, err
	}
	var known []string
	for _, s := range set {
		if s.Account == account {
			return s, nil
		}
		known = append(known, s.Account)
	}
	if len(known) == 0 {
		return source.CSV{}, fmt.Errorf("books: no source for %q; the log has none at all", account)
	}
	return source.CSV{}, fmt.Errorf("books: no source for %q; the log knows %v", account, known)
}

// LoadSources records the difference between a sources file and the log. Sources are unordered, so
// there is nothing to move.
func LoadSources(log *eventlog.Log, actor, why string, want []source.CSV) (LoadResult, error) {
	var result LoadResult

	seen := map[string]bool{}
	for _, s := range want {
		switch {
		case s.Account == "":
			return result, fmt.Errorf("books: a source has no account")
		case s.Currency == "":
			return result, fmt.Errorf("books: source %s has no currency", s.Account)
		case seen[s.Account]:
			return result, fmt.Errorf("books: two sources read %q; the account is a source's identity", s.Account)
		}
		seen[s.Account] = true
	}

	current, err := Sources(log)
	if err != nil {
		return result, err
	}
	currentByAccount := map[string]source.CSV{}
	for _, s := range current {
		currentByAccount[s.Account] = s
	}

	track := func(account, action string, data sourceData) error {
		data.Why = why
		body, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = log.Track(eventlog.Event{
			Collection: CollectionSource, RecordID: account, Action: action,
			Version: version, Actor: actor, Data: body,
		})
		return err
	}

	for _, s := range current {
		if !seen[s.Account] {
			if err := track(s.Account, ActionRemoved, sourceData{}); err != nil {
				return result, err
			}
			result.Removed++
		}
	}

	for _, s := range want {
		existing, known := currentByAccount[s.Account]
		switch {
		case !known:
			if err := track(s.Account, ActionAdded, fromSource(s)); err != nil {
				return result, err
			}
			result.Added++
		case existing != s:
			if err := track(s.Account, ActionChanged, fromSource(s)); err != nil {
				return result, err
			}
			result.Changed++
		}
	}
	return result, nil
}
