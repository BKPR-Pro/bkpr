// Package source turns the outside world into normalized transactions. CSV is the default
// transport: every bank exports it, it needs no credentials, and it works the same for everyone.
// Other sources (aggregators, bank APIs) are additional connectors behind the same contract.
package source

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/model"
)

// CSV is one account and how to read its statements. The account is its identity; the currency and
// the column layout belong to that account, not to whoever runs an import. Banks disagree about
// column names, date formats, and whether amounts are one signed column or a debit/credit pair, so
// this is configuration rather than code. A later OFX or aggregator source is a peer of this type.
type CSV struct {
	Account     string `json:"account"`     // ledger account this statement belongs to
	Currency    string `json:"currency"`    // written on every posting drawn from it
	Date        string `json:"date"`        // header of the date column
	Description string `json:"description"` // header of the memo column
	DateFormat  string `json:"date_format"` // Go layout, e.g. "2006-01-02"

	// Either a single signed Amount column, or a Debit/Credit pair.
	Amount string `json:"amount,omitempty"`
	Debit  string `json:"debit,omitempty"`
	Credit string `json:"credit,omitempty"`
}

var notAmount = regexp.MustCompile(`[^0-9.\-]`)

// ReadCSV parses a statement into normalized transactions.
func ReadCSV(r io.Reader, m CSV) ([]model.Transaction, error) {
	if m.Amount == "" && m.Debit == "" && m.Credit == "" {
		return nil, fmt.Errorf("source %s needs either an amount column or a debit/credit pair", m.Account)
	}

	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	index, err := indexHeader(header, m)
	if err != nil {
		return nil, err
	}

	var txs []model.Transaction
	for line := 2; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if blank(record) {
			continue
		}

		raw := map[string]string{}
		for name, i := range index {
			if i < len(record) {
				raw[name] = strings.TrimSpace(record[i])
			}
		}

		date, err := time.Parse(m.DateFormat, raw[m.Date])
		if err != nil {
			return nil, fmt.Errorf("line %d: date %q does not match format %q", line, raw[m.Date], m.DateFormat)
		}

		amount, err := amountFor(raw, m)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		txs = append(txs, model.Transaction{
			Account:     m.Account,
			Date:        date,
			Amount:      amount,
			Description: raw[m.Description],
			Raw:         raw,
		})
	}
	Identify(txs)
	return txs, nil
}

// indexHeader maps every column the mapping names onto its position, failing loudly when the
// export does not carry a column we were told to read.
func indexHeader(header []string, m CSV) (map[string]int, error) {
	positions := map[string]int{}
	for i, name := range header {
		positions[strings.TrimSpace(name)] = i
	}
	for _, required := range []string{m.Date, m.Description, m.Amount, m.Debit, m.Credit} {
		if required == "" {
			continue
		}
		if _, ok := positions[required]; !ok {
			return nil, fmt.Errorf("column %q is not in the CSV header %v", required, header)
		}
	}
	return positions, nil
}

// amountFor reads either the signed amount column or the debit/credit pair, in the account's
// commodity. The result is an integer Amount; no float is involved on the way in.
func amountFor(raw map[string]string, m CSV) (model.Amount, error) {
	if m.Amount != "" {
		return parseAmount(raw[m.Amount], m.Currency)
	}

	debit, err := parseAmount(raw[m.Debit], m.Currency)
	if err != nil {
		return model.Amount{}, err
	}
	credit, err := parseAmount(raw[m.Credit], m.Currency)
	if err != nil {
		return model.Amount{}, err
	}
	if !debit.IsZero() && !credit.IsZero() {
		return model.Amount{}, fmt.Errorf("row has both a debit (%s) and a credit (%s)", debit, credit)
	}
	if !debit.IsZero() {
		// A debit column holds a magnitude; money out is that magnitude, negative.
		if debit.Units > 0 {
			return debit.Negate(), nil
		}
		return debit, nil
	}
	return credit, nil
}

// parseAmount accepts the shapes banks actually emit: "$1,234.56", "(45.00)" for negatives,
// "+12", "-84.20", and empty (zero). It strips the presentation and parses the digits as integer
// minor units in the given commodity.
func parseAmount(s, commodity string) (model.Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return model.Amount{Commodity: commodity}, nil
	}

	negative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	cleaned := notAmount.ReplaceAllString(s, "")
	if cleaned == "" || cleaned == "-" {
		return model.Amount{}, fmt.Errorf("amount %q is not a number", s)
	}

	amount, err := model.NewAmount(cleaned, commodity)
	if err != nil {
		return model.Amount{}, fmt.Errorf("amount %q is not a number", s)
	}
	if negative && amount.Units > 0 {
		amount = amount.Negate()
	}
	return amount, nil
}

// Fingerprint is the stable identity of a normalized line: a hash of the account, date, amount, and
// normalized description. The same account and line always fingerprint the same, whatever source
// read them, so a CSV and a bank import of one account produce interchangeable ids.
func Fingerprint(account string, date time.Time, amount model.Amount, description string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		account,
		date.Format("2006-01-02"),
		amount.String(),
		strings.Join(strings.Fields(strings.ToLower(description)), " "),
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// Identify assigns each transaction its fingerprint-based id, in order, disambiguating genuinely
// identical lines on one day with a -N suffix so they stay distinct. A source builds transactions
// without an id and calls this once, so the idempotency root is computed one way for every source.
func Identify(txs []model.Transaction) {
	seen := map[string]int{}
	for i := range txs {
		fp := Fingerprint(txs[i].Account, txs[i].Date, txs[i].Amount, txs[i].Description)
		seen[fp]++
		txs[i].ID = fmt.Sprintf("%s-%d", fp, seen[fp])
	}
}

func blank(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}
