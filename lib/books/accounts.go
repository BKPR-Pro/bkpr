package books

import (
	"encoding/json"
	"fmt"

	"github.com/BKPR-Pro/bkpr/lib/eventlog"
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

// ImportAccountMeta records metadata for each account a source declared, and returns how many it
// recorded. An account whose stored metadata already carries every key/value being set is skipped,
// so re-importing the same file -- the shape a migration takes -- adds no event and the log does
// not grow on a re-run. Setting merges per key (see SetAccountMeta), so a source that names only an
// address never clears a name recorded elsewhere.
func ImportAccountMeta(log *eventlog.Log, actor string, accounts map[string]map[string]string) (int, error) {
	current, err := AccountMeta(log)
	if err != nil {
		return 0, err
	}
	recorded := 0
	for account, meta := range accounts {
		if len(meta) == 0 {
			continue
		}
		if metaSubset(meta, current[account]) {
			continue
		}
		if err := SetAccountMeta(log, actor, account, meta); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

// metaSubset reports whether every key in want is already present in have with the same value, so a
// set that would change nothing can be skipped.
func metaSubset(want, have map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// OwnedAccounts is the set of accounts you hold -- the real bank, card, and loan accounts money is
// read from -- as opposed to the income, expense, and equity categories money is assigned to. An
// account is yours if a statement was ever imported against it (it is some transaction's source
// account) or a connector posts to it. This is what tells an internal transfer between two of your
// accounts from a coincidental deposit, and what the account list and reconciliation iterate over.
func OwnedAccounts(log *eventlog.Log) (map[string]bool, error) {
	txs, err := Transactions(log)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, tx := range txs {
		owned[tx.Account] = true
	}
	conns, err := Connectors(log)
	if err != nil {
		return nil, err
	}
	for _, c := range conns {
		if c.Account != "" {
			owned[c.Account] = true
		}
	}
	// A routed entry moves its source leg off the transaction's own account onto a sub-account, so
	// that sub-account is one you hold too: it must show in the account list and reconcile, and the
	// balance sheet only counts a leg landing on an owned account. Fold the entries to learn where
	// each source leg actually lands, so a routing rule or categorization brings its child into view.
	_, entries, err := categorized(log)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Source != "" {
			owned[e.Source] = true
		}
	}
	return owned, nil
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
