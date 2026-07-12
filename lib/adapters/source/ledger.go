package source

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/model"
)

// ReadLedger parses plain-text ledger — the form bookkeeper writes back, or a hand-kept file —
// into statement lines. Each entry names the account it came from: its single amountless posting
// when one leg was elided, or its last posting when every leg is priced (ledger's convention puts
// the source account last). The line's amount is the negation of what the other postings account
// for, summed per commodity; a commodity whose legs cancel among themselves needs no price, and an
// entry where two commodities both leave a remainder is refused. Account directives, periodic (~)
// templates, and comments — whole-line or inline — are not statement lines and are dropped. The
// categorization itself is not carried in: import records the raw line, and the rules place it,
// keeping the books a fold rather than a pile of frozen assertions. Fingerprints are regenerated
// from the reconstructed line, so this is not a byte-identical round trip with the original import.
func ReadLedger(r io.Reader) ([]model.Transaction, error) {
	var txs []model.Transaction
	seen := map[string]int{} // fingerprint -> times seen, so identical lines stay distinct

	var (
		haveEntry bool
		skipBlock bool
		date      time.Time
		payee     string
		memo      string
		postings  []posting
		entryLine int
	)

	flush := func() error {
		if !haveEntry {
			return nil
		}
		// The memo note is the line's original description when the writer knew one; the entry
		// title is the payee a rule chose. The description is what fingerprints and rules key on,
		// so the memo wins when present.
		description := payee
		if memo != "" {
			description = memo
		}
		tx, err := reconstruct(date, description, postings, seen)
		if err != nil {
			return fmt.Errorf("ledger entry at line %d (%s): %w", entryLine, payee, err)
		}
		txs = append(txs, tx)
		haveEntry, memo, postings = false, "", nil
		return nil
	}

	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			skipBlock = false
			if err := flush(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#"):
			// A comment, except the one note the writer uses to carry a line's raw description.
			if haveEntry {
				if m, ok := strings.CutPrefix(trimmed, "; memo:"); ok {
					memo = strings.TrimSpace(m)
				}
			}
		case line[0] == ' ' || line[0] == '\t':
			// a posting under the current entry, or a sub-line of a skipped block
			if skipBlock {
				continue
			}
			if !haveEntry {
				return nil, fmt.Errorf("ledger line %d: a posting before any entry", n)
			}
			p, err := parsePosting(stripComment(trimmed))
			if err != nil {
				return nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			postings = append(postings, p)
		case strings.HasPrefix(line, "account ") || strings.HasPrefix(line, "~"):
			// a directive or a periodic template, not a statement line
			if err := flush(); err != nil {
				return nil, err
			}
			skipBlock = true
		default:
			// a new entry header; the previous entry ends here
			skipBlock = false
			if err := flush(); err != nil {
				return nil, err
			}
			d, pay, err := parseHeader(stripComment(trimmed))
			if err != nil {
				return nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			haveEntry, date, payee, entryLine = true, d, pay, n
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return txs, nil
}

// stripComment drops an inline "; ..." note from a header or posting line: everything after the
// amount (or the payee) is commentary, never data.
func stripComment(s string) string {
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// posting is one line of an entry: an account, and an amount unless it is the elided (balancing) one.
type posting struct {
	account string
	amount  model.Amount
	priced  bool
}

func parseHeader(s string) (time.Time, string, error) {
	sp := strings.IndexAny(s, " \t")
	if sp < 0 {
		return time.Time{}, "", fmt.Errorf("entry %q has no payee", s)
	}
	date, err := time.Parse("2006/01/02", s[:sp])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("entry date %q is not YYYY/MM/DD", s[:sp])
	}
	payee := strings.TrimSpace(s[sp:])
	payee = strings.TrimSpace(strings.TrimPrefix(payee, "*"))
	payee = strings.TrimSpace(strings.TrimPrefix(payee, "!"))
	return date, payee, nil
}

func parsePosting(s string) (posting, error) {
	// ledger separates the account from the amount by two or more spaces, or a tab.
	if i := twoSpaceGap(s); i >= 0 {
		account := strings.TrimSpace(s[:i])
		amount, err := model.ParseAmount(strings.TrimSpace(s[i:]))
		if err != nil {
			return posting{}, err
		}
		return posting{account: account, amount: amount, priced: true}, nil
	}
	return posting{account: s}, nil
}

// twoSpaceGap returns the index of the first run of two spaces or a tab, which ledger uses to
// separate an account (whose name may hold single spaces) from its amount.
func twoSpaceGap(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\t' || (s[i] == ' ' && s[i+1] == ' ') {
			return i
		}
	}
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		return i
	}
	return -1
}

func reconstruct(date time.Time, description string, postings []posting, seen map[string]int) (model.Transaction, error) {
	var elided []string
	sums := map[string]model.Amount{}
	var commodities []string // map iteration order is random; remainders must be reported stably
	for _, p := range postings {
		if !p.priced {
			elided = append(elided, p.account)
			continue
		}
		prev, ok := sums[p.amount.Commodity]
		if !ok {
			sums[p.amount.Commodity] = p.amount
			commodities = append(commodities, p.amount.Commodity)
			continue
		}
		next, err := prev.Add(p.amount)
		if err != nil {
			return model.Transaction{}, err
		}
		sums[p.amount.Commodity] = next
	}
	if len(sums) == 0 {
		return model.Transaction{}, fmt.Errorf("entry has no priced postings")
	}

	// a commodity whose legs cancel among themselves asks nothing of the statement account
	var remainders []model.Amount
	for _, c := range commodities {
		if !sums[c].IsZero() {
			remainders = append(remainders, sums[c])
		}
	}

	var account string
	var amount model.Amount
	switch {
	case len(elided) > 1:
		return model.Transaction{}, fmt.Errorf("need at most one amountless posting for the statement account, found %d", len(elided))
	case len(remainders) > 1:
		return model.Transaction{}, fmt.Errorf("cannot add %s and %s in one entry without a price", remainders[0].Commodity, remainders[1].Commodity)
	case len(elided) == 1:
		account = elided[0]
		amount = sums[commodities[0]].Negate()
		if len(remainders) == 1 {
			amount = remainders[0].Negate()
		}
	case len(remainders) != 0:
		return model.Transaction{}, fmt.Errorf("entry with every posting priced sums to %s, not zero", remainders[0])
	default:
		// fully priced and balanced: ledger's convention writes the source account last
		last := postings[len(postings)-1]
		account, amount = last.account, last.amount
	}
	fp := Fingerprint(account, date, amount, description)
	seen[fp]++

	return model.Transaction{
		ID:          fmt.Sprintf("%s-%d", fp, seen[fp]),
		Account:     account,
		Date:        date,
		Amount:      amount,
		Description: description,
	}, nil
}
