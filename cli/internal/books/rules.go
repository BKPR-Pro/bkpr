package books

import (
	"encoding/json"
	"fmt"

	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
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

// LoadResult reports what a rule file did to the log.
type LoadResult struct {
	Added, Changed, Removed, Moved int
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

// LoadRules records the difference between a rule file and the log as the per-rule intents that
// difference implies. The file is an editing surface, which is the right one for an ordered
// document; the log is the truth. Loading an unchanged file records nothing.
func LoadRules(log *eventlog.Log, actor, why string, want []rules.Rule) (LoadResult, error) {
	var result LoadResult

	seen := make(map[string]bool, len(want))
	for _, r := range want {
		if r.Match == "" {
			return result, fmt.Errorf("books: a rule has no match")
		}
		if seen[r.Match] {
			return result, fmt.Errorf("books: two rules match %q; the pattern is a rule's identity", r.Match)
		}
		seen[r.Match] = true
	}

	current, err := Rules(log)
	if err != nil {
		return result, err
	}

	track := func(match, action string, data ruleData) error {
		data.Why = why
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

	for _, r := range current {
		if !seen[r.Match] {
			if err := track(r.Match, ActionRemoved, ruleData{}); err != nil {
				return result, err
			}
			result.Removed++
		}
	}

	for _, r := range want {
		i := indexOf(current, r.Match)
		if i >= 0 && (current[i].Payee != r.Payee || current[i].Category != r.Category) {
			if err := track(r.Match, ActionChanged, ruleData{Payee: r.Payee, Category: r.Category}); err != nil {
				return result, err
			}
			result.Changed++
		}
	}

	// Right to left, so the rule a new one precedes is already in the set by the time it lands.
	for i := len(want) - 1; i >= 0; i-- {
		if indexOf(current, want[i].Match) >= 0 {
			continue
		}
		before := ""
		if i+1 < len(want) {
			before = want[i+1].Match
		}
		if err := track(want[i].Match, ActionAdded, ruleData{Payee: want[i].Payee, Category: want[i].Category, Before: before}); err != nil {
			return result, err
		}
		result.Added++
	}

	// Whatever order the adds and removes left behind, walk it into the file's order. Every rule in
	// want is now present, so this terminates with the two in agreement.
	now, err := Rules(log)
	if err != nil {
		return result, err
	}
	for i := range want {
		if now[i].Match == want[i].Match {
			continue
		}
		before := now[i].Match
		if err := track(want[i].Match, ActionMoved, ruleData{Before: before}); err != nil {
			return result, err
		}
		result.Moved++

		var moved rules.Rule
		now, moved = takeOut(now, want[i].Match)
		now = insertBefore(now, moved, before)
	}

	return result, nil
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
