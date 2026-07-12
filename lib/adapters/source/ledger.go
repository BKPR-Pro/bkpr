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
// templates, and comments — whole-line or inline — are not statement lines and are dropped.
//
// Alongside each line it returns the categorization the file already names: the entry's postings
// with the source account excluded, as a model.Entry parallel to the transaction. A file is
// categorized data, not a raw statement, so an import can assert that categorization rather than
// making the rules re-derive what the file plainly says. Fingerprints are regenerated from the
// reconstructed line, so this is not a byte-identical round trip with the original import.
func ReadLedger(r io.Reader) ([]model.Transaction, []model.Entry, error) {
	var txs []model.Transaction
	var entries []model.Entry // entries[i] is the categorization the file gave txs[i]
	seen := map[string]int{}  // fingerprint -> times seen, so identical lines stay distinct

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
		tx, side, err := reconstruct(date, description, postings, seen)
		if err != nil {
			return fmt.Errorf("ledger entry at line %d (%s): %w", entryLine, payee, err)
		}
		txs = append(txs, tx)
		// The entry title is the payee the file names, kept distinct from the memo the line
		// fingerprints on, so a carried assertion reads as the file wrote it.
		entries = append(entries, model.Entry{Payee: payee, Postings: side})
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
				return nil, nil, err
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
				return nil, nil, fmt.Errorf("ledger line %d: a posting before any entry", n)
			}
			p, err := parsePosting(stripComment(trimmed))
			if err != nil {
				return nil, nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			postings = append(postings, p)
		case strings.HasPrefix(line, "account ") || strings.HasPrefix(line, "~"):
			// a directive or a periodic template, not a statement line
			if err := flush(); err != nil {
				return nil, nil, err
			}
			skipBlock = true
		default:
			// a new entry header; the previous entry ends here
			skipBlock = false
			if err := flush(); err != nil {
				return nil, nil, err
			}
			d, pay, err := parseHeader(stripComment(trimmed))
			if err != nil {
				return nil, nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			haveEntry, date, payee, entryLine = true, d, pay, n
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if err := flush(); err != nil {
		return nil, nil, err
	}
	return txs, entries, nil
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

func reconstruct(date time.Time, description string, postings []posting, seen map[string]int) (model.Transaction, []model.Posting, error) {
	var elided []string
	var priced []posting // every posting that carries an amount, i.e. the categorized side
	sums := map[string]model.Amount{}
	var commodities []string // map iteration order is random; remainders must be reported stably
	for _, p := range postings {
		if !p.priced {
			elided = append(elided, p.account)
			continue
		}
		priced = append(priced, p)
		prev, ok := sums[p.amount.Commodity]
		if !ok {
			sums[p.amount.Commodity] = p.amount
			commodities = append(commodities, p.amount.Commodity)
			continue
		}
		next, err := prev.Add(p.amount)
		if err != nil {
			return model.Transaction{}, nil, err
		}
		sums[p.amount.Commodity] = next
	}
	if len(sums) == 0 {
		return model.Transaction{}, nil, fmt.Errorf("entry has no priced postings")
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
	var categorized []posting // the postings the file categorized the line into, the source excluded
	switch {
	case len(elided) > 1:
		return model.Transaction{}, nil, fmt.Errorf("need at most one amountless posting for the statement account, found %d", len(elided))
	case len(remainders) > 1:
		return model.Transaction{}, nil, fmt.Errorf("cannot add %s and %s in one entry without a price", remainders[0].Commodity, remainders[1].Commodity)
	case len(elided) == 1:
		// the source is the amountless posting, so every priced posting is categorization
		account = elided[0]
		amount = sums[commodities[0]].Negate()
		if len(remainders) == 1 {
			amount = remainders[0].Negate()
		}
		categorized = priced
	case len(remainders) != 0:
		return model.Transaction{}, nil, fmt.Errorf("entry with every posting priced sums to %s, not zero", remainders[0])
	default:
		// fully priced and balanced: ledger's convention writes the source account last, so every
		// posting before it is categorization
		last := postings[len(postings)-1]
		account, amount = last.account, last.amount
		categorized = postings[:len(postings)-1]
	}
	fp := Fingerprint(account, date, amount, description)
	seen[fp]++

	tx := model.Transaction{
		ID:          fmt.Sprintf("%s-%d", fp, seen[fp]),
		Account:     account,
		Date:        date,
		Amount:      amount,
		Description: description,
	}
	side := make([]model.Posting, 0, len(categorized))
	for _, p := range categorized {
		side = append(side, model.Posting{Account: p.account, Amount: p.amount})
	}
	return tx, side, nil
}
