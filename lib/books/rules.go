package books

import (
	"encoding/json"
	"fmt"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/rules"
)

const (
	// CollectionRule is keyed by a rule's match pattern, which is its identity. Changing what a
	// rule answers keeps that identity, so the history of a merchant's treatment stays together.
	// Changing the pattern is a different rule, and rightly loses the history, because it now
	// fires on a different set of lines.
	CollectionRule = "rule"

	ActionAdded   = "added"
	ActionChanged = "changed"
	ActionRemoved = "removed"
	ActionMoved   = "moved"
)

// ruleData is the payload of every rule event. Before names the rule this one precedes, because
// order is semantic: `city water` must beat `water`. An empty Before means last.
type ruleData struct {
	Payee    string `json:"payee,omitempty"`
	Category string `json:"category,omitempty"`
	Before   string `json:"before,omitempty"`
	Why      string `json:"why,omitempty"`
}

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
			set = insertBefore(set, rules.Rule{Match: e.RecordID, Payee: data.Payee, Category: data.Category}, data.Before)
		case ActionChanged:
			if i := indexOf(set, e.RecordID); i >= 0 {
				set[i].Payee, set[i].Category = data.Payee, data.Category
			}
		case ActionRemoved:
			set, _ = takeOut(set, e.RecordID)
		case ActionMoved:
			var r rules.Rule
			if set, r = takeOut(set, e.RecordID); r.Match != "" {
				set = insertBefore(set, r, data.Before)
			}
		}
	}
	return set, nil
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
	if indexOf(current, r.Match) >= 0 {
		return fmt.Errorf("books: a rule already matches %q; the pattern is its identity, so use `rules set`", r.Match)
	}
	if before != "" && indexOf(current, before) < 0 {
		return fmt.Errorf("books: no rule matches %q to place this one before", before)
	}
	return trackRule(log, actor, r.Match, ActionAdded, ruleData{Payee: r.Payee, Category: r.Category, Before: before})
}

// SetRule changes what an existing rule answers. This reclassifies every past line the rule
// matched, so it carries a why.
func SetRule(log *eventlog.Log, actor, why string, r rules.Rule) error {
	current, err := Rules(log)
	if err != nil {
		return err
	}
	if indexOf(current, r.Match) < 0 {
		return fmt.Errorf("books: no rule matches %q; add it first", r.Match)
	}
	return trackRule(log, actor, r.Match, ActionChanged, ruleData{Payee: r.Payee, Category: r.Category, Why: why})
}

// RemoveRule drops a rule. The lines it categorized fall back to whatever else matches, or to
// Uncategorized.
func RemoveRule(log *eventlog.Log, actor, match string) error {
	current, err := Rules(log)
	if err != nil {
		return err
	}
	if indexOf(current, match) < 0 {
		return fmt.Errorf("books: no rule matches %q", match)
	}
	return trackRule(log, actor, match, ActionRemoved, ruleData{})
}

// MoveRule reorders a rule. An empty before sends it to the end; otherwise it lands ahead of the
// named rule.
func MoveRule(log *eventlog.Log, actor, match, before string) error {
	current, err := Rules(log)
	if err != nil {
		return err
	}
	if indexOf(current, match) < 0 {
		return fmt.Errorf("books: no rule matches %q", match)
	}
	if before != "" && indexOf(current, before) < 0 {
		return fmt.Errorf("books: no rule matches %q to move before", before)
	}
	return trackRule(log, actor, match, ActionMoved, ruleData{Before: before})
}

func trackRule(log *eventlog.Log, actor, match, action string, data ruleData) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionRule, RecordID: match, Action: action,
		Version: version, Actor: actor, Data: body,
	})
	return err
}

func indexOf(set []rules.Rule, match string) int {
	for i, r := range set {
		if r.Match == match {
			return i
		}
	}
	return -1
}

// takeOut removes the rule and hands it back. A zero rule means it was not there.
func takeOut(set []rules.Rule, match string) ([]rules.Rule, rules.Rule) {
	i := indexOf(set, match)
	if i < 0 {
		return set, rules.Rule{}
	}
	r := set[i]
	return append(set[:i:i], set[i+1:]...), r
}

// insertBefore places the rule ahead of the named one. An unknown or empty anchor means last: a
// rule whose anchor was later removed still has a defined position.
func insertBefore(set []rules.Rule, r rules.Rule, before string) []rules.Rule {
	i := indexOf(set, before)
	if before == "" || i < 0 {
		return append(set, r)
	}
	out := append(set[:i:i], r)
	return append(out, set[i:]...)
}
