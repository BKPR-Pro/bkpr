package books

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/BKPR-Pro/bkpr/lib/costbasis"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
)

// CollectionPolicy is keyed by the account a cost-basis method applies to. The empty key is the
// book-wide default, which every account without its own setting falls back to. A method is
// authored knowledge (a jurisdiction's rule for an account), so like a rule or a connector it is a
// fact in the log rather than a config file.
const CollectionPolicy = "policy"

// ActionSet upserts a policy: recording it again under the same account replaces it, so the latest
// fact wins the way a re-categorization does.
const ActionSet = "set"

// BookDefault is the account marker callers use for the book-wide default: an empty account. It is
// stored under bookDefaultKey, because the log requires a non-empty record id.
const BookDefault = ""

// bookDefaultKey is the record id the book-wide default is stored under. It carries an "@" so it can
// never collide with a real ledger account path.
const bookDefaultKey = "@default"

// defaultMethod is the fallback when nothing is set: ACB, the rule for Canadian capital property.
const defaultMethod = "acb"

type policyData struct {
	Method string `json:"method"`
}

// SetPolicy records the cost-basis method for an account, or for the whole book when account is
// empty. The method is validated here, so an unknown name is refused before it reaches the log
// rather than surfacing later when a sale tries to fold its base.
func SetPolicy(log *eventlog.Log, actor, account, method string) error {
	if _, err := costbasis.ByName(method); err != nil {
		return fmt.Errorf("books: %w", err)
	}
	recordID := account
	if account == BookDefault {
		recordID = bookDefaultKey
	}
	data, err := json.Marshal(policyData{Method: method})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionPolicy, RecordID: recordID, Action: ActionSet,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// PolicySet is the folded cost-basis settings: a book-wide default and any per-account overrides.
type PolicySet struct {
	def       string
	byAccount map[string]string
}

// Policies folds the log into the current cost-basis settings. An empty key sets the default; any
// other key overrides one account. The latest fact for a key wins.
func Policies(log *eventlog.Log) (PolicySet, error) {
	events, err := log.All()
	if err != nil {
		return PolicySet{}, err
	}

	ps := PolicySet{def: defaultMethod, byAccount: map[string]string{}}
	for _, e := range events {
		if e.Collection != CollectionPolicy || e.Action != ActionSet {
			continue
		}
		var data policyData
		if err := e.Decode(&data); err != nil {
			return PolicySet{}, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		if e.RecordID == bookDefaultKey {
			ps.def = data.Method
			continue
		}
		ps.byAccount[e.RecordID] = data.Method
	}
	return ps, nil
}

// Method reports the cost-basis method name for an account: its own setting if it has one, else the
// book default.
func (ps PolicySet) Method(account string) string {
	if m, ok := ps.byAccount[account]; ok {
		return m
	}
	if ps.def == "" {
		return defaultMethod
	}
	return ps.def
}

// For resolves an account to the policy value the cost-basis fold applies to it. A stored name is
// validated when it is set, so this cannot see an unknown one; a zero PolicySet still answers ACB.
func (ps PolicySet) For(account string) costbasis.Policy {
	p, err := costbasis.ByName(ps.Method(account))
	if err != nil {
		return costbasis.ACB
	}
	return p
}

// PolicyLine is one folded setting, for listing.
type PolicyLine struct {
	Account string // empty for the book default
	Method  string
}

// PolicyList returns the settings for display: the book default first, then per-account overrides
// in account order.
func (ps PolicySet) PolicyList() []PolicyLine {
	out := []PolicyLine{{Account: BookDefault, Method: ps.Method(BookDefault)}}
	accounts := make([]string, 0, len(ps.byAccount))
	for a := range ps.byAccount {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)
	for _, a := range accounts {
		out = append(out, PolicyLine{Account: a, Method: ps.byAccount[a]})
	}
	return out
}
