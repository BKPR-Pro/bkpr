// Package rules is the deterministic first tier of categorization. It is fast, free, and
// reproducible: the same statement and the same rules always yield the same books. Rules are
// defaults, keyed by pattern, and they are always followed. They are never learned automatically,
// because a correction is real-world context about one charge, not evidence about a merchant.
package rules

import (
	"fmt"
	"regexp"

	"github.com/dallasread/bookkeeper/lib/model"
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

	// Metadata is an opaque bag the engine neither reads nor validates. Apply carries it onto the
	// entry, first-wins per key, so a connector can read its own namespaced keys (e.g.
	// rentapp.lease) off a categorized line without the core knowing what they mean.
	Metadata map[string]string `json:"metadata,omitempty"`

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

// Apply walks the rules in order, taking each field from the first rule that matches and supplies
// it, and turns the result into an entry. Every line posts: one no rule matches goes to
// Uncategorized rather than being withheld, or guessed into Expenses or Income.
func (e *Engine) Apply(tx model.Transaction) model.Entry {
	var payee, category string
	var metadata map[string]string

	// Every field is first-wins, so the walk cannot stop early: a later matching rule may still be
	// the first to supply a payee, a category, or a metadata key an earlier match left empty.
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
		for k, v := range r.Metadata {
			if _, taken := metadata[k]; taken {
				continue
			}
			if metadata == nil {
				metadata = map[string]string{}
			}
			metadata[k] = v
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
		Metadata: metadata,
	}
}
