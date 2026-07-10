// Package rules is the deterministic first tier of categorization. It is fast, free, and
// reproducible: the same statement and the same rules always yield the same books. Rules are
// defaults, keyed by pattern, and they are always followed. They are never learned automatically,
// because a correction is real-world context about one charge, not evidence about a merchant.
package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/dallasread/bookkeeper/cli/internal/model"
)

// Rule matches a transaction's description and supplies a payee, an account to post to, or both.
// Matching is case-insensitive.
//
// Rules are ordered, and for each field the first matching rule that supplies it wins. That is
// what lets a specific rule name the account while a later, broader rule names the payee.
//
// A rule that cannot name a whole account path stops at Uncategorized. A hardware store charge
// could serve any property, and that fact lives on the receipt rather than in the description, so
// the rule says Expenses:Real Estate:Materials:Uncategorized and says nothing it does not know.
type Rule struct {
	Match    string `json:"match"`
	Payee    string `json:"payee,omitempty"`
	Category string `json:"category,omitempty"`

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

// ReadFile reads a JSON rule set. The file is an editing surface, which is the right one for an
// ordered document; the log is the truth. Rules are data, not code, so a different set of books
// means a different rule set, not a different build.
func ReadFile(path string) ([]Rule, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs []Rule
	if err := json.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rs, nil
}

// Apply walks the rules in order, taking each field from the first rule that matches and supplies
// it, and turns the result into an entry. Every line posts: one no rule matches goes to
// Uncategorized rather than being withheld, or guessed into Expenses or Income.
func (e *Engine) Apply(tx model.Transaction) model.Entry {
	var payee, category string

	for _, r := range e.rules {
		if !r.re.MatchString(tx.Description) {
			continue
		}
		if payee == "" {
			payee = r.Payee
		}
		if category == "" {
			category = r.Category
		}
		if payee != "" && category != "" {
			break
		}
	}

	if payee == "" {
		payee = tx.Description
	}
	if category == "" {
		category = model.Uncategorized
	}

	return model.Entry{
		Payee:    payee,
		Postings: []model.Posting{{Account: category, Amount: tx.Amount.Negate()}},
	}
}
