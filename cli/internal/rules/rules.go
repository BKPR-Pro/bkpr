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
type Rule struct {
	Match    string `json:"match"`
	Payee    string `json:"payee,omitempty"`
	Category string `json:"category,omitempty"`

	// Uncertain marks a category that is a defensible default rather than a fact. An ambiguous
	// merchant (a hardware store that could serve any property) sets this: the line still posts to
	// Category, but it is flagged pending so it is easy to find and correct later. The attribution
	// lives in the real world, not in the description, so no amount of matching recovers it.
	Uncertain bool   `json:"uncertain,omitempty"`
	Reason    string `json:"reason,omitempty"`

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

// Apply walks the rules in order, taking each field from the first rule that matches and supplies
// it, and turns the result into an entry. Every line posts: one no rule categorizes goes to
// Suspense rather than being withheld, and one categorized by a default posts to that default.
// Both are flagged.
func (e *Engine) Apply(tx model.Transaction) model.Entry {
	var payee, category, reason string
	var uncertain bool

	for _, r := range e.rules {
		if !r.re.MatchString(tx.Description) {
			continue
		}
		if payee == "" {
			payee = r.Payee
		}
		// Guard on r.Category so the uncertainty of the rule that actually supplied the account is
		// not cleared by a later rule that matched for some other field.
		if category == "" && r.Category != "" {
			category = r.Category
			uncertain, reason = r.Uncertain, r.Reason
		}
		if payee != "" && category != "" {
			break
		}
	}

	entry := model.Entry{Payee: payee}
	if entry.Payee == "" {
		entry.Payee = tx.Description
	}

	switch {
	case category == "":
		entry.Postings = []model.Posting{{Account: model.SuspenseAccount, AmountCents: -tx.AmountCents}}
		entry.Pending = true
		entry.Reason = "no rule supplied a category"
	case uncertain:
		entry.Postings = []model.Posting{{Account: category, AmountCents: -tx.AmountCents}}
		entry.Pending = true
		entry.Reason = reason
		if entry.Reason == "" {
			entry.Reason = "category is a default; confirm the attribution"
		}
	default:
		entry.Postings = []model.Posting{{Account: category, AmountCents: -tx.AmountCents}}
	}
	return entry
}
