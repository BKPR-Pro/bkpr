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

// AddSource records how to read one account's statements. It is an upsert keyed by the account,
// which is the source's identity: a new account is added, and giving an account bookkeeper already
// knows changes how it is read. An identical repeat records nothing.
func AddSource(log *eventlog.Log, actor string, s source.CSV) error {
	switch {
	case s.Account == "":
		return fmt.Errorf("books: a source needs an account")
	case s.Currency == "":
		return fmt.Errorf("books: source %s needs a currency", s.Account)
	case s.Amount == "" && s.Debit == "" && s.Credit == "":
		return fmt.Errorf("books: source %s needs an amount column or a debit/credit pair", s.Account)
	}

	current, err := Sources(log)
	if err != nil {
		return err
	}
	action := ActionAdded
	for _, existing := range current {
		if existing.Account == s.Account {
			if existing == s {
				return nil // no change
			}
			action = ActionChanged
			break
		}
	}
	return trackSource(log, actor, s.Account, action, fromSource(s))
}

// RemoveSource forgets how to read an account. Lines already imported from it stay in the books;
// only future imports lose their source.
func RemoveSource(log *eventlog.Log, actor, account string) error {
	current, err := Sources(log)
	if err != nil {
		return err
	}
	for _, s := range current {
		if s.Account == account {
			return trackSource(log, actor, account, ActionRemoved, sourceData{})
		}
	}
	return fmt.Errorf("books: no source for %q", account)
}

func trackSource(log *eventlog.Log, actor, account, action string, data sourceData) error {
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
