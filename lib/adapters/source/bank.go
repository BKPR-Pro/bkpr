package source

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/model"
)

// A bank is another source, imported like a CSV or a ledger file but read from the bank's website
// instead of a file, for the Canadian institutions that expose no free transaction API (RBC,
// Simplii, PC Financial). The browser automation lives outside Go, in a per-institution Playwright
// script run with Node, so the binary stays stdlib-only: Go runs the script with the account's
// details in the environment, and the script prints the lines as JSON, which ReadBank normalizes and
// fingerprints exactly like a CSV. The credential is a saved browser session, not a stored password:
// the script reuses it headless and, when it has expired and a person is present, signs in again in a
// headed browser and saves the fresh session. The shared session-and-sign-in logic is scripts/
// harness.js; each institution script is just its selectors. What remains per institution is pinning
// those selectors to the real site (marked TODO in each). See docs/importing-from-banks.md.

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

// Bank is one account to import from a bank: which institution to drive, the account's URL and the
// ledger account its lines land in, the browser session that reaches it, and the default currency.
// DefaultCurrency is a fallback: a line that names its own currency keeps it. The session -- not a
// stored password -- is the credential: SessionFile is where it is saved and reused, keyed by the
// connector's token-env so several accounts on one login share it. Interactive says a person is
// present to sign in again when the session has expired; Relogin forces a fresh sign-in.
type Bank struct {
	Institution     string
	Account         string
	DefaultCurrency string
	LoginURL        string
	SessionFile     string
	Interactive     bool
	Relogin         bool

	// CredentialRefs names, per field (username, password, and any security answers), where that
	// secret lives -- a reference like "op://Private/RBC/password", never the secret. SecretCmd is the
	// command that turns a reference into its value ("op read {}" by default). They are resolved at
	// import and the values handed to the browser script over stdin; nothing secret is stored here, on
	// the command line, or in the environment. Absent credentials mean an interactive (headed) sign-in.
	CredentialRefs map[string]string
	SecretCmd      string

	// SnapshotDir, when set, is where the script saves the HTML of the page it read, as
	// <institution>.html, for diagnosing a run that read nothing or the wrong thing. The directory is
	// made self-ignoring because the page holds real statement data. Empty disables snapshots.
	SnapshotDir string

	// AccountPath is the ordered list of link or button labels to click, after signing in, to reach
	// this account -- for a bank whose accounts have no stable URL (RBC: "Go to RBC Business Banking",
	// then "Current Account"). Empty means the LoginURL itself is the account, as for a CSV-like bank.
	AccountPath []string

	// Progress, when set, is called with the script's current stage (starting a browser, signing in,
	// reading transactions) so a caller can show a live status. It is called from the goroutine that
	// reads the script's output, synchronously, during the run.
	Progress func(stage string)

	// HistoryDays, when > 0, asks the script to read that many days back rather than the site's short
	// default (RBC's presets stop at 30 days). The window is turned into a from/to date pair here --
	// the script drives the site's custom date-range filter -- so "today" is decided once, in Go.
	HistoryDays int
}

// ErrSessionExpired reports that a bank's saved session is gone and no person was present to sign in
// again (the script exited EX_TEMPFAIL). The caller turns it into a "sign in again" hint rather than
// a generic failure.
var ErrSessionExpired = errors.New("bank session expired")

// runBank runs an institution's browser script with the resolved credentials and returns its JSON
// output. It is a package variable so tests can drive ReadBank without Node or a browser.
var runBank = execBankScript

// BankResult is what one account's import yields: its transactions, and -- when the site showed it --
// the account's current balance, ground truth for reconciliation. HasBalance is false when the script
// returned rows only.
type BankResult struct {
	Transactions []model.Transaction
	Balance      model.Amount
	HasBalance   bool
}

// ReadBank imports one account's transactions from its bank, normalized and fingerprinted the same
// way ReadCSV is, so a bank import and a CSV of one account are interchangeable and idempotent. It
// also carries the account's scraped balance when the script reported one.
func ReadBank(b Bank) (BankResult, error) {
	if !SupportsBank(b.Institution) {
		return BankResult{}, fmt.Errorf("import: no bank importer for %q; known: %v", b.Institution, Banks())
	}
	creds, err := resolveCredentials(b.CredentialRefs, b.SecretCmd)
	if err != nil {
		return BankResult{}, err
	}
	out, err := runBank(b, creds)
	if err != nil {
		return BankResult{}, err
	}
	return parseBankOutput(out, b.Account, b.DefaultCurrency)
}

// exitSessionExpired is the script's EX_TEMPFAIL exit: a real session that has expired, with no
// person present to sign in again. It maps to ErrSessionExpired.
const exitSessionExpired = 75

// execBankScript materializes the embedded scripts into a temp dir -- so an institution script can
// require('./harness.js'), the shared session-and-sign-in code -- and runs its entry with Node,
// passing the account's details in the environment. NODE_PATH is set so the script resolves the
// customer's Playwright (global or project-local). Resolved credentials, when present, are handed to
// the script as a JSON object on stdin -- never on the command line or in the environment, where
// another process could read them -- so an unattended sign-in can type them; with none, the script
// falls back to the saved session or a headed human sign-in.
func execBankScript(b Bank, creds map[string]string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "bk-bank-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	entries, err := bankScripts.ReadDir("scripts")
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	for _, e := range entries {
		data, err := bankScripts.ReadFile("scripts/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("import: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600); err != nil {
			return nil, err
		}
	}

	cmd := exec.Command("node", filepath.Join(dir, filepath.Base(bankScript[b.Institution])))
	cmd.Env = append(os.Environ(),
		"BK_IMPORT_URL="+b.LoginURL,
		"BK_IMPORT_ACCOUNT="+b.Account,
		"BK_IMPORT_CURRENCY="+b.DefaultCurrency,
		"BK_IMPORT_SESSION_FILE="+b.SessionFile,
		"BK_IMPORT_INTERACTIVE="+boolEnv(b.Interactive),
		"BK_IMPORT_RELOGIN="+boolEnv(b.Relogin),
		"NODE_PATH="+nodePath(),
	)
	if b.SnapshotDir != "" {
		cmd.Env = append(cmd.Env, "BK_IMPORT_SNAPSHOT_FILE="+filepath.Join(b.SnapshotDir, b.Institution+".html"))
	}
	if len(b.AccountPath) > 0 {
		pathJSON, err := json.Marshal(b.AccountPath)
		if err != nil {
			return nil, err
		}
		cmd.Env = append(cmd.Env, "BK_IMPORT_ACCOUNT_PATH="+string(pathJSON))
	}
	if b.HistoryDays > 0 {
		now := time.Now()
		// RBC's date fields read "MMM D, YYYY" (its own "Example: Feb 17, 2020"), matching how it
		// prints transaction dates.
		const rbcDate = "Jan 2, 2006"
		cmd.Env = append(cmd.Env,
			"BK_IMPORT_HISTORY_FROM="+now.AddDate(0, 0, -b.HistoryDays).Format(rbcDate),
			"BK_IMPORT_HISTORY_TO="+now.Format(rbcDate),
		)
	}
	if creds == nil {
		creds = map[string]string{}
	}
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(credsJSON)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("import %s: needs Node and Playwright to drive the browser (node not found)", b.Institution)
		}
		return nil, fmt.Errorf("import %s: %w", b.Institution, err)
	}
	// Read stderr to completion -- routing progress markers to b.Progress and keeping the rest for a
	// failure message -- before waiting on the process, as StderrPipe requires.
	errText := strings.TrimSpace(scanProgress(stderrPipe, b.Progress))
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == exitSessionExpired {
			return nil, fmt.Errorf("import %s: %w", b.Institution, ErrSessionExpired)
		}
		if errText != "" {
			return nil, fmt.Errorf("import %s: %s", b.Institution, errText)
		}
		return nil, fmt.Errorf("import %s: %w", b.Institution, err)
	}
	return stdout.Bytes(), nil
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// nodePath assembles a NODE_PATH so a script materialized in a temp dir can resolve the customer's
// Playwright: any existing NODE_PATH, the project-local node_modules, and the global modules dir when
// npm reports one. It is best-effort; the harness prints a clear message if Playwright is still not
// found.
func nodePath() string {
	var parts []string
	if p := os.Getenv("NODE_PATH"); p != "" {
		parts = append(parts, p)
	}
	if cwd, err := os.Getwd(); err == nil {
		parts = append(parts, filepath.Join(cwd, "node_modules"))
	}
	if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			parts = append(parts, root)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
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

// bankOutput is what a script prints: the account's rows and, optionally, its current balance. For a
// script that reports rows only, a bare JSON array is also accepted and taken as the rows.
type bankOutput struct {
	Rows    []bankRow `json:"rows"`
	Balance string    `json:"balance"`
}

// parseBankOutput turns the script's JSON into normalized, fingerprinted transactions and, when the
// script reported one, the account's current balance in the account's currency.
func parseBankOutput(data []byte, account, defaultCurrency string) (BankResult, error) {
	data = bytes.TrimSpace(data)
	var out bankOutput
	if len(data) > 0 && data[0] == '[' { // a bare array: rows only, no balance
		if err := json.Unmarshal(data, &out.Rows); err != nil {
			return BankResult{}, fmt.Errorf("import: reading the bank script's output: %w", err)
		}
	} else if err := json.Unmarshal(data, &out); err != nil {
		return BankResult{}, fmt.Errorf("import: reading the bank script's output: %w", err)
	}

	txs := make([]model.Transaction, 0, len(out.Rows))
	for i, r := range out.Rows {
		date, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return BankResult{}, fmt.Errorf("import: row %d: date %q must be YYYY-MM-DD", i+1, r.Date)
		}
		currency := r.Currency
		if currency == "" {
			currency = defaultCurrency
		}
		amount, err := model.NewAmount(r.Amount, currency)
		if err != nil {
			return BankResult{}, fmt.Errorf("import: row %d: %w", i+1, err)
		}
		txs = append(txs, model.Transaction{
			Account: account, Date: date, Amount: amount, Description: r.Description,
		})
	}
	Identify(txs)

	res := BankResult{Transactions: txs}
	if bal := strings.TrimSpace(out.Balance); bal != "" {
		amount, err := model.NewAmount(bal, defaultCurrency)
		if err != nil {
			return BankResult{}, fmt.Errorf("import: reading the balance %q: %w", bal, err)
		}
		res.Balance, res.HasBalance = amount, true
	}
	return res, nil
}
