package books

import (
	"encoding/json"
	"fmt"

	"github.com/dallasread/bkpr/lib/eventlog"
	"github.com/dallasread/bkpr/lib/model"
	"github.com/dallasread/bkpr/lib/rules"
)

const (
	// CollectionRule is keyed by a rule's identity: its match pattern, and -- when it has one -- its
	// amount predicate, since a predicate narrows the set of lines the rule fires on and so is part
	// of what makes it that rule. Changing what a rule answers keeps that identity, so the history of
	// a merchant's treatment stays together. Changing the pattern or the amount is a different rule,
	// and rightly loses the history, because it now fires on a different set of lines. An amountless
	// rule keys on its pattern alone, so logs written before amount predicates read unchanged.
	CollectionRule = "rule"

	ActionAdded   = "added"
	ActionChanged = "changed"
	ActionRemoved = "removed"
	ActionMoved   = "moved"
)

// ruleData is the payload of every rule event. Before names the rule this one precedes, because
// order is semantic: `city water` must beat `water`. An empty Before means last. Match and Amount
// carry the rule's identity in the payload too, because the record id is now a composite key; a
// legacy event has no Match here and the record id is its pattern, so the fold falls back to it.
type ruleData struct {
	Match       string            `json:"match,omitempty"`
	Amount      *model.Amount     `json:"amount,omitempty"`
	Payee       string            `json:"payee,omitempty"`
	Category    string            `json:"category,omitempty"`
	Source      string            `json:"source,omitempty"`
	TaxRate     string            `json:"tax_rate,omitempty"`
	TaxAccount  string            `json:"tax_account,omitempty"`
	TaxCategory string            `json:"tax_category,omitempty"`
	TaxFrom     string            `json:"tax_from,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Before      string            `json:"before,omitempty"`
	Why         string            `json:"why,omitempty"`
}

// ruleKey is a rule's identity as a string: its pattern, plus its amount predicate when it has one.
// The amount is canonicalized to a magnitude with no trailing zeros, so a rule set at "175.00 CAD"
// keys the same as one an rm names as "175 CAD". The NUL separator cannot occur in a pattern, so a
// composite key never collides with a bare one.
func ruleKey(match string, amount *model.Amount) string {
	if amount == nil {
		return match
	}
	a := *amount
	if a.Units < 0 {
		a = a.Negate()
	}
	for a.Scale > 0 && a.Units%10 == 0 {
		a.Units /= 10
		a.Scale--
	}
	return match + "\x00" + a.String()
}

// keyOf is the identity of a whole rule.
func keyOf(r rules.Rule) string { return ruleKey(r.Match, r.Amount) }

// SameRule reports whether two rules are the same rule -- the same pattern and the same amount
// predicate -- so a caller can find the rule an edit targets without knowing how identity is keyed.
func SameRule(a, b rules.Rule) bool { return keyOf(a) == keyOf(b) }

// Rules folds the log into the current rule set, in order.
func Rules(log *eventlog.Log) ([]rules.Rule, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	var set []rules.Rule
	for _, e := range events {
		if e.Collection != CollectionRule {
			continue
		}
		var data ruleData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}

		switch e.Action {
		case ActionAdded:
			// A merged log can carry an added for an identity already present, because each book
			// authored the rule on its own. The identity is the rule's key, so the second added reads
			// as a change in place — the later book's answer — never as a second rule.
			if i := indexOfKey(set, e.RecordID); i >= 0 {
				set[i] = ruleFrom(e.RecordID, data)
			} else {
				set = insertBefore(set, ruleFrom(e.RecordID, data), data.Before)
			}
		case ActionChanged:
			if i := indexOfKey(set, e.RecordID); i >= 0 {
				set[i] = ruleFrom(e.RecordID, data)
			}
		case ActionRemoved:
			set, _ = takeOutKey(set, e.RecordID)
		case ActionMoved:
			var r rules.Rule
			if set, r = takeOutKey(set, e.RecordID); r.Match != "" {
				set = insertBefore(set, r, data.Before)
			}
		}
	}
	return set, nil
}

// ruleFrom rebuilds a rule from an event's record id and payload. The pattern and amount come from
// the payload; a legacy event that stored neither there keys its pattern in the record id, so an
// empty payload Match falls back to it. Added and changed carry the whole rule, so folding one is a
// full replace, not a field-by-field merge.
func ruleFrom(recordID string, data ruleData) rules.Rule {
	match := data.Match
	if match == "" {
		match = recordID
	}
	return rules.Rule{
		Match: match, Amount: data.Amount, Payee: data.Payee, Category: data.Category, Source: data.Source,
		TaxRate: data.TaxRate, TaxAccount: data.TaxAccount, TaxCategory: data.TaxCategory, TaxFrom: data.TaxFrom,
		Metadata: data.Metadata,
	}
}

// dataFrom is the inverse: the payload an add or change event stores for a rule. It carries the
// pattern and amount too, since the record id is now a composite key rather than the pattern.
func dataFrom(r rules.Rule, before, why string) ruleData {
	return ruleData{
		Match: r.Match, Amount: r.Amount, Payee: r.Payee, Category: r.Category, Source: r.Source,
		TaxRate: r.TaxRate, TaxAccount: r.TaxAccount, TaxCategory: r.TaxCategory, TaxFrom: r.TaxFrom,
		Metadata: r.Metadata, Before: before, Why: why,
	}
}

// AddRule records a new rule. The match pattern is the rule's identity, so a pattern already in
// the set is an error rather than a silent overwrite; use SetRule to change what a rule answers.
// A new rule lands at the end unless before names the rule it should precede, since order decides
// which of two matching rules wins.
func AddRule(log *eventlog.Log, actor string, r rules.Rule, before string) error {
	if r.Match == "" {
		return fmt.Errorf("books: a rule needs a match")
	}
	current, err := Rules(log)
	if err != nil {
		return err
	}
	if indexOfKey(current, keyOf(r)) >= 0 {
		return fmt.Errorf("books: a rule already matches %q at that amount; its pattern and amount are its identity, so use `rules set`", r.Match)
	}
	if before != "" && indexOfPattern(current, before) < 0 {
		return fmt.Errorf("books: no rule matches %q to place this one before", before)
	}
	return trackRule(log, actor, keyOf(r), ActionAdded, dataFrom(r, before, ""))
}

// SetRule changes what an existing rule answers. This reclassifies every past line the rule
// matched, so it carries a why.
func SetRule(log *eventlog.Log, actor, why string, r rules.Rule) error {
	current, err := Rules(log)
	if err != nil {
		return err
	}
	if indexOfKey(current, keyOf(r)) < 0 {
		return fmt.Errorf("books: no rule matches %q at that amount; add it first", r.Match)
	}
	return trackRule(log, actor, keyOf(r), ActionChanged, dataFrom(r, "", why))
}

// RemoveRule drops a rule, named by its pattern and (when it has one) its amount predicate. The
// lines it categorized fall back to whatever else matches, or to Uncategorized.
func RemoveRule(log *eventlog.Log, actor, match string, amount *model.Amount) error {
	current, err := Rules(log)
	if err != nil {
		return err
	}
	key := ruleKey(match, amount)
	if indexOfKey(current, key) < 0 {
		return fmt.Errorf("books: no rule matches %q", match)
	}
	return trackRule(log, actor, key, ActionRemoved, ruleData{})
}

// MoveRule reorders a rule, named by its pattern and (when it has one) its amount predicate. An
// empty before sends it to the end; otherwise it lands ahead of the named rule.
func MoveRule(log *eventlog.Log, actor, match string, amount *model.Amount, before string) error {
	current, err := Rules(log)
	if err != nil {
		return err
	}
	if indexOfKey(current, ruleKey(match, amount)) < 0 {
		return fmt.Errorf("books: no rule matches %q", match)
	}
	if before != "" && indexOfPattern(current, before) < 0 {
		return fmt.Errorf("books: no rule matches %q to move before", before)
	}
	return trackRule(log, actor, ruleKey(match, amount), ActionMoved, ruleData{Before: before})
}

func trackRule(log *eventlog.Log, actor, recordID, action string, data ruleData) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionRule, RecordID: recordID, Action: action,
		Version: version, Actor: actor, Data: body,
	})
	return err
}

// indexOfKey finds a rule by its full identity (pattern and amount).
func indexOfKey(set []rules.Rule, key string) int {
	for i, r := range set {
		if keyOf(r) == key {
			return i
		}
	}
	return -1
}

// indexOfPattern finds the first rule with a pattern, ignoring its amount. It is what a `before`
// anchor names: order among amount-variants of one pattern does not matter (they are mutually
// exclusive), so anchoring to the pattern is enough.
func indexOfPattern(set []rules.Rule, match string) int {
	for i, r := range set {
		if r.Match == match {
			return i
		}
	}
	return -1
}

// takeOutKey removes the rule with an identity and hands it back. A zero rule means it was not there.
func takeOutKey(set []rules.Rule, key string) ([]rules.Rule, rules.Rule) {
	i := indexOfKey(set, key)
	if i < 0 {
		return set, rules.Rule{}
	}
	r := set[i]
	return append(set[:i:i], set[i+1:]...), r
}

// insertBefore places the rule ahead of the first one on the named pattern. An unknown or empty
// anchor means last: a rule whose anchor was later removed still has a defined position.
func insertBefore(set []rules.Rule, r rules.Rule, before string) []rules.Rule {
	i := indexOfPattern(set, before)
	if before == "" || i < 0 {
		return append(set, r)
	}
	out := append(set[:i:i], r)
	return append(out, set[i:]...)
}
