package source

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dallasread/bkpr/lib/model"
)

// ReadLedger parses plain-text ledger — the form bkpr writes back, or a hand-kept file —
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
// reconstructed line under scope -- the ledger file's own name, the door the lines entered
// through -- so this is not a byte-identical round trip with the original import.
func ReadLedger(r io.Reader, scope string) ([]model.Transaction, []model.Entry, error) {
	var txs []model.Transaction
	var entries []model.Entry // entries[i] is the categorization the file gave txs[i]
	seen := map[string]int{}  // fingerprint -> times seen, so identical lines stay distinct

	var (
		haveEntry     bool
		skipBlock     bool
		date          time.Time
		invoice       string
		payee         string
		pending       bool
		memo          string
		postings      []posting
		blockComments []string
		entryLine     int
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
		tx, side, routedSource, err := reconstruct(scope, date, description, postings, seen)
		if err != nil {
			return fmt.Errorf("ledger entry at line %d (%s): %w", entryLine, payee, err)
		}
		txs = append(txs, tx)
		// The entry title is the payee the file names, kept distinct from the memo the line
		// fingerprints on, so a carried assertion reads as the file wrote it. Standalone notes inside
		// the entry ride along as block comments, in the order the file wrote them. The pending flag is
		// the entry's own accounting state, carried back so a "!" line is not re-asserted cleared. A
		// routed source leg (one the file sent to a sub-account, named by a "registered:" tag) is
		// carried as the entry's Source, so re-import lands the leg on the same child.
		entries = append(entries, model.Entry{Payee: payee, Invoice: invoice, Pending: pending, Postings: side, Source: routedSource, BlockComments: blockComments})
		haveEntry, invoice, pending, memo, postings, blockComments = false, "", false, "", nil, nil
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
			// A comment. The one the writer uses to carry a line's raw description is the memo; any
			// other note inside an entry is a standalone block comment the person kept beside the line.
			// A comment outside any entry (under an account directive, say) belongs to nothing and is
			// dropped.
			if haveEntry {
				if m, ok := strings.CutPrefix(trimmed, "; memo:"); ok {
					memo = strings.TrimSpace(m)
				} else {
					blockComments = append(blockComments, strings.TrimSpace(trimmed[1:]))
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
			code, comment := splitComment(trimmed)
			p, err := parsePosting(code)
			if err != nil {
				return nil, nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			p.comment = comment
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
			d, inv, pay, pend, err := parseHeader(stripComment(trimmed))
			if err != nil {
				return nil, nil, fmt.Errorf("ledger line %d: %w", n, err)
			}
			haveEntry, date, invoice, payee, pending, entryLine = true, d, inv, pay, pend, n
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

// ReadLedgerAccounts collects the account directives a ledger file declares, as authored knowledge
// about the accounts themselves rather than about any transaction. Each `account NAME` block's run
// of `address` sub-lines folds, in file order, into one "address" value -- a multi-line letterhead
// kept whole -- so the result is account -> metadata, the shape books.SetAccountMeta records. A
// directive with no sub-lines carries no facts and so contributes no entry. Everything else in the
// file (entries, periodic templates, comments) is not a directive and is ignored here; ReadLedger
// reads those. It is a second, independent pass, so callers that only want statement lines are
// untouched.
func ReadLedgerAccounts(r io.Reader) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	var account string
	var address []string

	flush := func() {
		if account != "" && len(address) > 0 {
			out[account] = map[string]string{"address": strings.Join(address, "\n")}
		}
		account, address = "", nil
	}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "account "):
			flush()
			account = strings.TrimSpace(line[len("account "):])
		case account != "" && (line == "" || line[0] != ' ' && line[0] != '\t'):
			// the directive's block ends at the first line that is not one of its indented sub-lines
			flush()
		case account != "":
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "address "); ok {
				address = append(address, strings.TrimSpace(rest))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}

// stripComment drops an inline "; ..." note from a header line: a comment on the entry header has
// no posting to belong to, so everything after the payee is commentary the reader discards.
func stripComment(s string) string {
	code, _ := splitComment(s)
	return code
}

// splitComment separates a line's code from its inline "; ..." note, returning the code trimmed of
// trailing space and the note trimmed of surrounding space. On a posting the note is that leg's
// comment, kept as data; on a header stripComment throws it away. A line with no ";" yields an
// empty note.
func splitComment(s string) (code, note string) {
	if i := strings.IndexByte(s, ';'); i >= 0 {
		note = strings.TrimSpace(s[i+1:])
		s = s[:i]
	}
	return strings.TrimRight(s, " \t"), note
}

// posting is one line of an entry: an account, and an amount unless it is the elided (balancing) one.
// A cross-commodity leg also carries the total price the "@@" form gave it, the cost basis a later
// sale reads.
type posting struct {
	account string
	amount  model.Amount
	cost    *model.Amount
	comment string
	priced  bool
}

// value is what the posting contributes toward balancing the line. A plain posting contributes its
// own amount; a priced posting contributes its total cost, signed to follow the quantity, so shares
// acquired add the cash they cost and shares disposed subtract the cash they raised. It mirrors
// model.Posting's own valuation, the two kept in step.
func (p posting) value() model.Amount {
	if p.cost == nil {
		return p.amount
	}
	c := *p.cost
	if c.Units < 0 {
		c = c.Negate()
	}
	if p.amount.Units < 0 {
		return c.Negate()
	}
	return c
}

// parseHeader reads an entry header into its date, payee, and clearing state. A leading "!" marks
// the entry pending (an accrued invoice, an uncleared payment) and a leading "*" or no flag marks it
// cleared; the flag is stripped from the payee either way, so what follows -- a transaction code like
// "(2073)" and the name -- is the payee.
func parseHeader(s string) (time.Time, string, string, bool, error) {
	sp := strings.IndexAny(s, " \t")
	if sp < 0 {
		return time.Time{}, "", "", false, fmt.Errorf("entry %q has no payee", s)
	}
	date, err := time.Parse("2006/01/02", s[:sp])
	if err != nil {
		return time.Time{}, "", "", false, fmt.Errorf("entry date %q is not YYYY/MM/DD", s[:sp])
	}
	payee := strings.TrimSpace(s[sp:])
	pending := false
	if rest, ok := strings.CutPrefix(payee, "!"); ok {
		pending, payee = true, strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(payee, "*"); ok {
		payee = strings.TrimSpace(rest)
	}
	// A leading "(code)" is the ledger transaction code -- an invoice or bill number -- lifted into
	// its own field so it is data, not text buried in the payee. Only a code that closes on the same
	// line is one; a lone "(" is left as part of the name.
	invoice := ""
	if strings.HasPrefix(payee, "(") {
		if end := strings.IndexByte(payee, ')'); end > 0 {
			invoice = strings.TrimSpace(payee[1:end])
			payee = strings.TrimSpace(payee[end+1:])
		}
	}
	return date, invoice, payee, pending, nil
}

func parsePosting(s string) (posting, error) {
	// ledger separates the account from the amount by two or more spaces, or a tab.
	if i := twoSpaceGap(s); i >= 0 {
		account := strings.TrimSpace(s[:i])
		amount, cost, err := model.ParsePosting(strings.TrimSpace(s[i:]), "")
		if err != nil {
			return posting{}, err
		}
		return posting{account: account, amount: amount, cost: cost, priced: true}, nil
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

func reconstruct(scope string, date time.Time, description string, postings []posting, seen map[string]int) (model.Transaction, []model.Posting, string, error) {
	var elided []posting // the amountless leg(s); at most one is allowed, and it is the source account
	var priced []posting // every posting that carries an amount, i.e. the categorized side
	sums := map[string]model.Amount{}
	var commodities []string // map iteration order is random; remainders must be reported stably
	for _, p := range postings {
		if !p.priced {
			elided = append(elided, p)
			continue
		}
		priced = append(priced, p)
		// A priced leg balances at its value in the line's own commodity: a plain leg is its amount,
		// a cross-commodity leg is its cost. Summing the cost, not the share quantity, is what lets a
		// share or property leg cancel against the cash that funded it instead of standing as its own
		// unbalanced commodity.
		v := p.value()
		prev, ok := sums[v.Commodity]
		if !ok {
			sums[v.Commodity] = v
			commodities = append(commodities, v.Commodity)
			continue
		}
		next, err := prev.Add(v)
		if err != nil {
			return model.Transaction{}, nil, "", err
		}
		sums[v.Commodity] = next
	}
	if len(sums) == 0 {
		return model.Transaction{}, nil, "", fmt.Errorf("entry has no priced postings")
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
	var sourceComment string  // the note on the source leg, the one the books infer
	var categorized []posting // the postings the file categorized the line into, the source excluded
	switch {
	case len(elided) > 1:
		return model.Transaction{}, nil, "", fmt.Errorf("need at most one amountless posting for the statement account, found %d", len(elided))
	case len(remainders) > 1:
		return model.Transaction{}, nil, "", fmt.Errorf("cannot add %s and %s in one entry without a price", remainders[0].Commodity, remainders[1].Commodity)
	case len(elided) == 1:
		// the source is the amountless posting, so every priced posting is categorization
		account = elided[0].account
		sourceComment = elided[0].comment
		amount = sums[commodities[0]].Negate()
		if len(remainders) == 1 {
			amount = remainders[0].Negate()
		}
		categorized = priced
	case len(remainders) != 0:
		return model.Transaction{}, nil, "", fmt.Errorf("entry with every posting priced sums to %s, not zero", remainders[0])
	default:
		// fully priced and balanced: ledger's convention writes the source account last, so every
		// posting before it is categorization
		last := postings[len(postings)-1]
		account, amount, sourceComment = last.account, last.amount, last.comment
		categorized = postings[:len(postings)-1]
	}

	// A routed leg names, in a "registered:" tag, the account the line was imported on. The account
	// shown on the leg is the purpose child the charge routed to; the tag holds the parent it was
	// imported against. Restore both, so the round trip preserves the routing rather than collapsing
	// the charge onto the child. A leg with no tag -- a hand-kept child line -- is left exactly as
	// written, its own account.
	var routedSource string
	if registered, rest, ok := cutRegisteredTag(sourceComment); ok && registered != "" {
		routedSource = account
		account = registered
		sourceComment = rest
	}

	fp := Fingerprint(scope, date, amount, description)
	seen[fp]++

	tx := model.Transaction{
		ID:          fmt.Sprintf("%s-%d", fp, seen[fp]),
		Account:     account,
		Date:        date,
		Amount:      amount,
		Description: description,
		Comment:     sourceComment,
	}
	side := make([]model.Posting, 0, len(categorized))
	for _, p := range categorized {
		side = append(side, model.Posting{Account: p.account, Amount: p.amount, Cost: p.cost, Comment: p.comment})
	}
	return tx, side, routedSource, nil
}

// registeredTag marks the inline note on a routed source leg that names the account the line was
// imported on. The writer in package ledger emits the same marker; the two are a matched pair.
const registeredTag = "registered:"

// cutRegisteredTag splits a source leg's note into the account the line was imported on and the
// writer's own note, when a "registered: <account>" tag is present. The writer joins any note and the
// tag with " ; ", the tag last, so this finds the segment that starts with the marker and returns the
// rest as the plain comment. Without the tag the whole note is the comment and ok is false.
func cutRegisteredTag(comment string) (registered, rest string, ok bool) {
	parts := strings.Split(comment, " ; ")
	for i, p := range parts {
		if acct, found := strings.CutPrefix(p, registeredTag+" "); found {
			others := make([]string, 0, len(parts)-1)
			others = append(others, parts[:i]...)
			others = append(others, parts[i+1:]...)
			return strings.TrimSpace(acct), strings.TrimSpace(strings.Join(others, " ; ")), true
		}
	}
	return "", comment, false
}
