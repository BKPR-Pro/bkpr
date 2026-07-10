// Package rules is the deterministic first tier of categorization. It is fast, free, and
// reproducible: the same statement always yields the same books. Only lines no rule matches
// escalate to the next tier, and every escalated answer comes back as a rule, so this tier
// grows and the expensive tiers shrink.
package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/dallasread/bookkeeper/cli/internal/model"
)

// Rule matches a transaction's description and supplies any subset of payee, category, and
// balance account. Matching is case-insensitive.
//
// Rules are ordered, and for each field the first matching rule that supplies it wins. That is
// what lets a specific rule name the payee and category while a trailing catch-all supplies the
// account's default balancing posting.
type Rule struct {
	Match    string `json:"match"`
	Payee    string `json:"payee,omitempty"`
	Category string `json:"category,omitempty"`
	Balance  string `json:"balance,omitempty"`

	re *regexp.Regexp
}

// Engine applies an ordered rule set.
type Engine struct {
	rules []Rule
}

// New compiles the rules. An invalid pattern is an error here rather than a surprise later.
func New(rs []Rule) (*Engine, error) {
	compiled := make([]Rule, len(rs))
	for i, r := range rs {
		if r.Match == "" {
			return nil, fmt.Errorf("rule %d: match is required", i)
		}
		re, err := regexp.Compile("(?i)" + r.Match)
		if err != nil {
			return nil, fmt.Errorf("rule %d (%q): %w", i, r.Match, err)
		}
		r.re = re
		compiled[i] = r
	}
	return &Engine{rules: compiled}, nil
}

// Load reads a JSON rule set from disk. Rules are data, not code, so a different set of books
// means a different file, not a different build.
func Load(path string) (*Engine, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs []Rule
	if err := json.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return New(rs)
}

// Apply walks the rules in order, filling each field of the decision from the first rule that
// matches and supplies it. A transaction that ends up with no category is never guessed at: it
// is handed to the human.
func (e *Engine) Apply(tx model.Transaction) model.Decision {
	var d model.Decision

	for _, r := range e.rules {
		if !r.re.MatchString(tx.Description) {
			continue
		}
		if d.Payee == "" {
			d.Payee = r.Payee
		}
		if d.Category == "" {
			d.Category = r.Category
		}
		if d.Balance == "" {
			d.Balance = r.Balance
		}
		if d.Payee != "" && d.Category != "" && d.Balance != "" {
			break
		}
	}

	// The transaction already knows which account its statement came from, so that is the natural
	// balancing posting. A rule only needs to name one when the entry is a transfer somewhere else.
	if d.Balance == "" {
		d.Balance = tx.Account
	}

	if d.Category == "" {
		d.NeedsReview = true
		d.Reason = "no rule supplied a category"
	}
	return d
}
