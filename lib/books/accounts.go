package books

import (
	"encoding/json"
	"fmt"

	"github.com/dallasread/bookkeeper/lib/eventlog"
)

// CollectionAccount is keyed by an account path. It carries metadata about the account itself, not
// about any one transaction: a business address for a letterhead, a customer's mailing address, a
// display name. Like a rule or a connector it is authored knowledge, so it is a fact in the log
// rather than a config file, and it folds the same way.
const CollectionAccount = "account"

type accountData struct {
	Meta map[string]string `json:"meta,omitempty"`
}

// SetAccountMeta attaches metadata to an account, merged per key: a call sets only the keys it
// names, so an address recorded now and a name recorded later both survive. Well-known keys are
// "name" and "address"; the bag is open, the way a rule's metadata is.
func SetAccountMeta(log *eventlog.Log, actor, account string, meta map[string]string) error {
	if account == "" {
		return fmt.Errorf("books: an account is required")
	}
	if len(meta) == 0 {
		return fmt.Errorf("books: nothing to set on %s", account)
	}
	data, err := json.Marshal(accountData{Meta: meta})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionAccount, RecordID: account, Action: ActionSet,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// AccountMeta folds the log into each account's merged metadata. Keys merge across events and the
// latest value of a key wins, so metadata accretes the way corrections do.
func AccountMeta(log *eventlog.Log) (map[string]map[string]string, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	out := map[string]map[string]string{}
	for _, e := range events {
		if e.Collection != CollectionAccount || e.Action != ActionSet {
			continue
		}
		var data accountData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		merged := out[e.RecordID]
		if merged == nil {
			merged = map[string]string{}
			out[e.RecordID] = merged
		}
		for k, v := range data.Meta {
			merged[k] = v
		}
	}
	return out, nil
}
