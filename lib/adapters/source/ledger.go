package source

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/model"
)

// ReadLedger parses the plain-text ledger form bookkeeper writes back into statement lines. Each
// entry names the account it came from as its single amountless posting (the one the writer elided),
// and the line's amount is the negation of what the priced postings account for. The categorization
// itself is not carried in: import records the raw line, and the rules place it, keeping the books a
// fold rather than a pile of frozen assertions. Fingerprints are regenerated from the reconstructed
// line, so this is not a byte-identical round trip with the original import.
func ReadLedger(r io.Reader) ([]model.Transaction, error) {
	var txs []model.Transaction
	seen := map[string]int{} // fingerprint -> times seen, so identical lines stay distinct

	var (
		haveEntry bool
		date      time.Time
		payee     string
		postings  []posting
		entryLine int
	)

	flush := func() error {
		if !haveEntry {
			return nil
		}
		tx, err := reconstruct(date, payee, postings, seen)
		if err != nil {
			return fmt.Errorf("ledger entry at line %d (%s): %w", entryLine, payee, err)
		}
		txs = append(txs, tx)
		haveEntry, postings = false, nil
		return nil
	}

	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			if err := flush(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#"):
			// a comment
		case line[0] == ' ' || line[0] == '\t':
			// a posting under the current entry
			if !haveEntry {
				return nil, fmt.Errorf("ledger line %d: a posting before any entry", n)
			}
			p, err := parsePosting(trimmed)
			if err != nil {
				return nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			postings = append(postings, p)
		default:
			// a new entry header; the previous entry ends here
			if err := flush(); err != nil {
				return nil, err
			}
			d, pay, err := parseHeader(trimmed)
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

func reconstruct(date time.Time, payee string, postings []posting, seen map[string]int) (model.Transaction, error) {
	var elided []string
	sum := model.Amount{}
	first := true
	for _, p := range postings {
		if !p.priced {
			elided = append(elided, p.account)
			continue
		}
		if first {
			sum = model.Amount{Commodity: p.amount.Commodity}
			first = false
		}
		next, err := sum.Add(p.amount)
		if err != nil {
			return model.Transaction{}, err
		}
		sum = next
	}

	switch {
	case len(elided) != 1:
		return model.Transaction{}, fmt.Errorf("need exactly one amountless posting for the statement account, found %d", len(elided))
	case first:
		return model.Transaction{}, fmt.Errorf("entry has no priced postings")
	}

	account := elided[0]
	amount := sum.Negate()
	fp := Fingerprint(account, date, amount, payee)
	seen[fp]++

	return model.Transaction{
		ID:          fmt.Sprintf("%s-%d", fp, seen[fp]),
		Account:     account,
		Date:        date,
		Amount:      amount,
		Description: payee,
	}, nil
}
