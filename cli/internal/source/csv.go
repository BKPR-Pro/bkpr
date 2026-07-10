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
	"strconv"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/cli/internal/model"
)

// Mapping describes how one institution's CSV export lines up with a Transaction. Banks disagree
// about column names, date formats, and whether amounts are one signed column or a debit/credit
// pair, so this is configuration rather than code.
type Mapping struct {
	Account     string `json:"account"`     // ledger account this statement belongs to
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
func ReadCSV(r io.Reader, m Mapping) ([]model.Transaction, error) {
	if m.Amount == "" && m.Debit == "" && m.Credit == "" {
		return nil, fmt.Errorf("mapping needs either an amount column or a debit/credit pair")
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
	seen := map[string]int{} // fingerprint -> times seen, so identical lines stay distinct

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

		cents, err := amountCents(raw, m)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		description := raw[m.Description]
		fingerprint := fingerprint(m.Account, date, cents, description)
		seen[fingerprint]++

		txs = append(txs, model.Transaction{
			ID:          fmt.Sprintf("%s-%d", fingerprint, seen[fingerprint]),
			Account:     m.Account,
			Date:        date,
			AmountCents: cents,
			Description: description,
			Raw:         raw,
		})
	}
	return txs, nil
}

// indexHeader maps every column the mapping names onto its position, failing loudly when the
// export does not carry a column we were told to read.
func indexHeader(header []string, m Mapping) (map[string]int, error) {
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

// amountCents reads either the signed amount column or the debit/credit pair. Money is integer
// cents everywhere; floats never touch a ledger.
func amountCents(raw map[string]string, m Mapping) (int64, error) {
	if m.Amount != "" {
		return parseCents(raw[m.Amount])
	}

	debit, err := parseCents(raw[m.Debit])
	if err != nil {
		return 0, err
	}
	credit, err := parseCents(raw[m.Credit])
	if err != nil {
		return 0, err
	}
	if debit != 0 && credit != 0 {
		return 0, fmt.Errorf("row has both a debit (%d) and a credit (%d)", debit, credit)
	}
	if debit != 0 {
		return -abs(debit), nil
	}
	return credit, nil
}

// parseCents accepts the shapes banks actually emit: "$1,234.56", "(45.00)" for negatives,
// "+12", "-84.20", and empty (zero).
func parseCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	negative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	cleaned := notAmount.ReplaceAllString(s, "")
	if cleaned == "" || cleaned == "-" {
		return 0, fmt.Errorf("amount %q is not a number", s)
	}

	value, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q is not a number", s)
	}

	cents := int64(value*100 + copysign(0.5, value)) // round half away from zero
	if negative {
		cents = -abs(cents)
	}
	return cents, nil
}

func fingerprint(account string, date time.Time, cents int64, description string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		account,
		date.Format("2006-01-02"),
		strconv.FormatInt(cents, 10),
		strings.Join(strings.Fields(strings.ToLower(description)), " "),
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

func blank(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func copysign(magnitude, sign float64) float64 {
	if sign < 0 {
		return -magnitude
	}
	return magnitude
}
