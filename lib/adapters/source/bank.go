package source

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/dallasread/bookkeeper/lib/model"
)

// A bank is another source, imported like a CSV or a ledger file but read from the bank's website
// instead of a file, for the Canadian institutions that expose no free transaction API (RBC,
// Simplii, PC Financial). The browser automation lives outside Go, in a per-institution Playwright
// script run with Node, so the binary stays stdlib-only: Go runs the script with the account's
// details in the environment, and the script prints the lines as JSON, which ReadBank normalizes and
// fingerprints exactly like a CSV. The scripts are stubs today, so an import fails loudly until one
// is written and a manual CSV export is the way in. See docs/importing-from-banks.md.

//go:embed scripts/*.js
var bankScripts embed.FS

// bankScript names the Playwright file each institution uses. Several accounts at one bank (an RBC
// card, chequing, and line of credit) share its script and differ only by the connector's URL and
// account.
var bankScript = map[string]string{
	"rbc":         "scripts/rbc.js",
	"simplii":     "scripts/simplii.js",
	"pcfinancial": "scripts/pcfinancial.js",
}

// SupportsBank reports whether a connector kind is a bank this package can import from.
func SupportsBank(kind string) bool {
	_, ok := bankScript[kind]
	return ok
}

// Banks lists the importable bank kinds, for help text.
func Banks() []string {
	return []string{"pcfinancial", "rbc", "simplii"} // sorted, so help reads stably
}

// Bank is one account to import from a bank: which institution to drive, the site and account to
// reach, the secret to sign in with (supplied from the environment, never stored), and the default
// currency. DefaultCurrency is a fallback: a line that names its own currency keeps it.
type Bank struct {
	Institution     string
	Account         string
	DefaultCurrency string
	LoginURL        string
	Secret          string
}

// runBank runs an institution's browser script and returns its JSON output. It is a package variable
// so tests can drive ReadBank without Node or a browser.
var runBank = execBankScript

// ReadBank imports one account's transactions from its bank, normalized and fingerprinted the same
// way ReadCSV is, so a bank import and a CSV of one account are interchangeable and idempotent.
func ReadBank(b Bank) ([]model.Transaction, error) {
	if !SupportsBank(b.Institution) {
		return nil, fmt.Errorf("import: no bank importer for %q; known: %v", b.Institution, Banks())
	}
	out, err := runBank(b)
	if err != nil {
		return nil, err
	}
	return parseBankRows(out, b.Account, b.DefaultCurrency)
}

// execBankScript writes the embedded script to a temp file and runs it with Node, passing the
// account's details in the environment. The secret goes in an environment variable, never on the
// command line, so it does not show up in a process listing.
func execBankScript(b Bank) ([]byte, error) {
	src, err := bankScripts.ReadFile(bankScript[b.Institution])
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	f, err := os.CreateTemp("", "bk-bank-*.js")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(src); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	cmd := exec.Command("node", f.Name())
	cmd.Env = append(os.Environ(),
		"BK_IMPORT_URL="+b.LoginURL,
		"BK_IMPORT_ACCOUNT="+b.Account,
		"BK_IMPORT_CURRENCY="+b.DefaultCurrency,
		"BK_IMPORT_SECRET="+b.Secret,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("import %s: needs Node and Playwright to drive the browser (node not found)", b.Institution)
		}
		if msg := bytes.TrimSpace(stderr.Bytes()); len(msg) > 0 {
			return nil, fmt.Errorf("import %s: %s", b.Institution, msg)
		}
		return nil, fmt.Errorf("import %s: %w", b.Institution, err)
	}
	return stdout.Bytes(), nil
}

// bankRow is one line as the script prints it. The amount is a plain signed decimal string
// ("-62.40"), which the script normalizes; the currency is optional and takes the account's default
// when absent.
type bankRow struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency,omitempty"`
}

// parseBankRows turns the script's JSON into normalized, fingerprinted transactions.
func parseBankRows(data []byte, account, defaultCurrency string) ([]model.Transaction, error) {
	var rows []bankRow
	if err := json.Unmarshal(bytes.TrimSpace(data), &rows); err != nil {
		return nil, fmt.Errorf("import: reading the bank script's output: %w", err)
	}

	txs := make([]model.Transaction, 0, len(rows))
	for i, r := range rows {
		date, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return nil, fmt.Errorf("import: row %d: date %q must be YYYY-MM-DD", i+1, r.Date)
		}
		currency := r.Currency
		if currency == "" {
			currency = defaultCurrency
		}
		amount, err := model.NewAmount(r.Amount, currency)
		if err != nil {
			return nil, fmt.Errorf("import: row %d: %w", i+1, err)
		}
		txs = append(txs, model.Transaction{
			Account: account, Date: date, Amount: amount, Description: r.Description,
		})
	}
	Identify(txs)
	return txs, nil
}
