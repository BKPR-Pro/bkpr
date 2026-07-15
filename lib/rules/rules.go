// Package rules is the deterministic first tier of categorization. It is fast, free, and
// reproducible: the same statement and the same rules always yield the same books. Rules are
// defaults, keyed by pattern, and they are always followed. They are never learned automatically,
// because a correction is real-world context about one charge, not evidence about a merchant.
package rules

import (
	"fmt"
	"regexp"
	"time"

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

	// Source, when set, routes the elided source (card, liability) leg to a sub-account instead of the
	// transaction's own registered account. A physical card imported on one account is then split by
	// purpose: each matching charge's card leg self-routes to its purpose child, while Category still
	// names where the offset lands. It is first-wins per field like the others, and it moves where the
	// leg is booked, never how much, so the entry still accounts for the whole line. Empty leaves the
	// leg on the account the line came from.
	Source string `json:"source,omitempty"`

	// Amount, when set, narrows the rule to lines of one magnitude: several payees that share a memo
	// but differ only by amount (a property tax that is $175 for one house and $155 for another)
	// become distinct rules on one pattern. It matches the line's magnitude, so the statement's
	// debit/credit sign is not part of it. A nil Amount does not constrain, and a rule's identity is
	// its pattern together with this predicate -- the set of lines it fires on.
	Amount *model.Amount `json:"amount,omitempty"`

	// TaxRate and TaxAccount make a vendor a taxed one: its charge already includes sales tax, so
	// Apply extracts the tax from the total (tax-inclusive, net = total / (1 + rate)) and posts it
	// to TaxAccount, leaving the pre-tax amount on the category. The rate is a percentage string
	// ("15%"); the two are required together, since a rate has nowhere to go without an account.
	TaxRate    string `json:"tax_rate,omitempty"`
	TaxAccount string `json:"tax_account,omitempty"`

	// TaxCategory scopes the tax to the categories it matches (case-insensitively, like Match),
	// because the right treatment can depend on the category rather than the vendor: the same
	// hardware store sells to a property whose tax is claimable and to one whose is not. The gate
	// reads the category the line finally takes, wherever that came from, and a category outside
	// the scope stays gross. Its capture groups may appear in TaxAccount ($1, ${name}), so one rule
	// derives each property's tax account from the category it matched. Empty scopes nothing out.
	TaxCategory string `json:"tax_category,omitempty"`

	// TaxFrom bounds the tax by date, "2006-01-02": a line dated before it stays gross. A rule's
	// split is a read-time fold over all history, and a filed year's lines already carry their
	// splits, so an unbounded retroactive split would double-count them. Empty splits over all time.
	TaxFrom string `json:"tax_from,omitempty"`

	// Metadata is an opaque bag the engine neither reads nor validates. Apply carries it onto the
	// entry, first-wins per key, so a connector can read its own namespaced keys (e.g.
	// rentapp.lease) off a categorized line without the core knowing what they mean.
	Metadata map[string]string `json:"metadata,omitempty"`

	re             *regexp.Regexp
	taxCategoryRe  *regexp.Regexp
	taxNum, taxDen int64
	taxFrom        time.Time
}

// Engine applies an ordered rule set.
type Engine struct {
	rules []Rule
}

// amountMatches reports whether a line of amount got satisfies a rule's amount predicate want. Both
// are taken as magnitudes, so a debit and the same-sized credit match one predicate, and the
// commodities must agree (Equal is commodity-sensitive), so 175 USD does not answer a 175 CAD rule.
func amountMatches(want, got model.Amount) bool {
	if want.Units < 0 {
		want = want.Negate()
	}
	if got.Units < 0 {
		got = got.Negate()
	}
	return want.Equal(got)
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
		if r.TaxRate != "" || r.TaxAccount != "" {
			if r.TaxRate == "" {
				return nil, fmt.Errorf("rule %d (%q): a tax account needs a tax rate", i, r.Match)
			}
			if r.TaxAccount == "" {
				return nil, fmt.Errorf("rule %d (%q): a tax rate needs a tax account", i, r.Match)
			}
			num, den, err := model.ParsePercent(r.TaxRate)
			if err != nil {
				return nil, fmt.Errorf("rule %d (%q): %w", i, r.Match, err)
			}
			r.taxNum, r.taxDen = num, den
		}
		if (r.TaxCategory != "" || r.TaxFrom != "") && r.taxDen == 0 {
			return nil, fmt.Errorf("rule %d (%q): a tax scope needs a tax; give -tax-rate and -tax-account", i, r.Match)
		}
		if r.TaxCategory != "" {
			if r.taxCategoryRe, err = regexp.Compile("(?i)" + r.TaxCategory); err != nil {
				return nil, fmt.Errorf("rule %d (%q): %w", i, r.Match, err)
			}
		}
		if r.TaxFrom != "" {
			if r.taxFrom, err = time.Parse("2006-01-02", r.TaxFrom); err != nil {
				return nil, fmt.Errorf("rule %d (%q): a tax from-date reads as 2006-01-02: %w", i, r.Match, err)
			}
		}
		compiled[i] = r
	}
	return &Engine{rules: compiled}, nil
}

// Apply walks the rules in order, taking each field from the first rule that matches and supplies
// it, and turns the result into an entry. Every line posts: one no rule matches goes to
// Uncategorized rather than being withheld, or guessed into Expenses or Income.
func (e *Engine) Apply(tx model.Transaction) model.Entry {
	var payee, category, source string
	var metadata map[string]string

	// Every field is first-wins, so the walk cannot stop early: a later matching rule may still be
	// the first to supply a payee, a category, or a metadata key an earlier match left empty.
	for _, r := range e.rules {
		if !r.matches(tx) {
			continue
		}
		if payee == "" {
			payee = r.Payee
		}
		if category == "" {
			category = r.Category
		}
		if source == "" {
			source = r.Source
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

	// The statement's sign is from the source account's point of view, so the categorized side takes
	// the opposite one. A taxed vendor splits that total into the pre-tax amount on the category and
	// the tax on its own account; both sum back to the line, so the entry stays balanced. The tax
	// gates on the category the walk resolved, so a scoped tax reads the line's final category even
	// when a different rule supplied it.
	full := tx.Amount.Negate()
	postings := []model.Posting{{Account: category, Amount: full}}
	if r := e.taxRule(tx); r != nil {
		if split, ok := r.taxSplit(tx.Date, category, full); ok {
			postings = split
		}
	}

	return model.Entry{
		Payee:    payee,
		Postings: postings,
		Source:   source,
		Metadata: metadata,
	}
}

// matches reports whether a rule fires on a line: its pattern on the description, and its amount
// predicate, when it has one, on the magnitude.
func (r Rule) matches(tx model.Transaction) bool {
	if !r.re.MatchString(tx.Description) {
		return false
	}
	return r.Amount == nil || amountMatches(*r.Amount, tx.Amount)
}

// taxRule finds the rule whose tax a line takes: the tax is first-wins like every field, so it is
// the first matching rule that supplies one, whether or not that rule also names the category.
func (e *Engine) taxRule(tx model.Transaction) *Rule {
	for i := range e.rules {
		r := &e.rules[i]
		if r.taxDen != 0 && r.matches(tx) {
			return r
		}
	}
	return nil
}

// taxSplit divides an amount on an account into the pre-tax posting on that account and the tax
// posting on the rule's tax account, or reports that this rule's tax does not touch it: the line is
// dated before TaxFrom, or the account falls outside TaxCategory. A scoped tax account may reference
// the scope's capture groups, so the account the tax lands on can be derived from the category.
func (r Rule) taxSplit(date time.Time, account string, amount model.Amount) ([]model.Posting, bool) {
	if r.taxDen == 0 {
		return nil, false
	}
	if !r.taxFrom.IsZero() && date.Before(r.taxFrom) {
		return nil, false
	}
	taxAccount := r.TaxAccount
	if r.taxCategoryRe != nil {
		m := r.taxCategoryRe.FindStringSubmatchIndex(account)
		if m == nil {
			return nil, false
		}
		taxAccount = string(r.taxCategoryRe.ExpandString(nil, r.TaxAccount, account, m))
	}
	net, tax := amount.SplitInclusive(r.taxNum, r.taxDen)
	return []model.Posting{{Account: account, Amount: net}, {Account: taxAccount, Amount: tax}}, true
}
