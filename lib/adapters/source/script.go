package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/model"
)

// A script is another source, imported like a CSV or a ledger file but read from a website instead of
// a file, for a system that exposes no free transaction API. The browser automation lives outside Go,
// in a per-kind Playwright script run with Node, so the binary stays stdlib-only: Go runs the script
// with the account's details in the environment, and the script prints the lines as JSON, which
// ReadScript normalizes and fingerprints exactly like a CSV. The credential is a saved browser
// session, not a stored password: the script reuses it headless and, when it has expired and a person
// is present, signs in again in a headed browser and saves the fresh session. The shared
// session-and-sign-in logic is the scripts dir's harness.js; each kind's script is just its selectors.
// The scripts are user-provided: drop a <kind>.js (and harness.js) into the scripts dir.

// SupportsScript reports whether a connector kind has an installed browser script.
func SupportsScript(kind, scriptsDir string) bool {
	if scriptsDir == "" || kind == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(scriptsDir, kind+".js"))
	return err == nil
}

// Scripts lists the installed script kinds (the *.js basenames in scriptsDir, minus harness), for help
// text.
func Scripts(scriptsDir string) []string {
	if scriptsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		return nil
	}
	var kinds []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".js") {
			continue
		}
		kind := strings.TrimSuffix(name, ".js")
		if kind == "harness" {
			continue
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

// Script is one account to import through a browser script: which kind to drive, where its scripts
// live, the account's URL and the ledger account its lines land in, the browser session that reaches
// it, and the default currency. DefaultCurrency is a fallback: a line that names its own currency
// keeps it. The session -- not a stored password -- is the credential: SessionFile is where it is
// saved and reused, keyed by the connector's token-env so several accounts on one login share it.
// Interactive says a person is present to sign in again when the session has expired; Relogin forces a
// fresh sign-in.
type Script struct {
	Name            string // the connector's name, the scope its lines fingerprint under
	Kind            string
	ScriptsDir      string
	Account         string
	DefaultCurrency string
	LoginURL        string
	SessionFile     string
	Interactive     bool
	Relogin         bool

	// CredentialRefs names, per field (username, password, and any security answers), where that
	// secret lives -- a reference like "op://Private/Acme/password", never the secret. SecretCmd is the
	// command that turns a reference into its value ("op read {}" by default). They are resolved at
	// import and the values handed to the browser script over stdin; nothing secret is stored here, on
	// the command line, or in the environment. Absent credentials mean an interactive (headed) sign-in.
	CredentialRefs map[string]string
	SecretCmd      string

	// SnapshotDir, when set, is where the script saves the HTML of the page it read, as <kind>.html,
	// for diagnosing a run that read nothing or the wrong thing. The directory is made self-ignoring
	// because the page holds real statement data. Empty disables snapshots.
	SnapshotDir string

	// AccountPath is the ordered list of link or button labels to click, after signing in, to reach
	// this account -- for a source whose accounts have no stable URL. Empty means the LoginURL itself is
	// the account, as for a CSV-like source.
	AccountPath []string

	// Progress, when set, is called with the script's current stage (starting a browser, signing in,
	// reading transactions) so a caller can show a live status. It is called from the goroutine that
	// reads the script's output, synchronously, during the run.
	Progress func(stage string)

	// HistoryDays, when > 0, asks the script to read that many days back rather than the site's short
	// default. The relative window is turned into a from/to date pair here, so "today" is decided once,
	// in Go.
	HistoryDays int

	// HistoryFrom and HistoryTo are an explicit date range ("MMM D, YYYY"), for a backfill of a known
	// period. When HistoryFrom is set it wins over HistoryDays; HistoryTo empty means up to today.
	HistoryFrom string
	HistoryTo   string
}

// ErrSessionExpired reports that a saved session is gone and no person was present to sign in again
// (the script exited EX_TEMPFAIL). The caller turns it into a "sign in again" hint rather than a
// generic failure.
var ErrSessionExpired = errors.New("session expired")

// runScript runs a kind's browser script with the resolved credentials and returns its JSON output. It
// is a package variable so tests can drive ReadScript without Node or a browser.
var runScript = execScript

// ScriptResult is what one account's import yields: its transactions, and -- when the site showed it
// -- the account's current balance, ground truth for reconciliation. HasBalance is false when the
// script returned rows only.
type ScriptResult struct {
	Transactions []model.Transaction
	Balance      model.Amount
	HasBalance   bool
}

// ReadScript imports one account's transactions through its browser script, normalized and
// fingerprinted under the connector's name the way ReadCSV fingerprints under a file's, so re-reading
// the same connector is idempotent whatever account its lines land in. It also carries the account's
// scraped balance when the script reported one.
func ReadScript(s Script) (ScriptResult, error) {
	if !SupportsScript(s.Kind, s.ScriptsDir) {
		return ScriptResult{}, fmt.Errorf("import: no script for kind %q in %s", s.Kind, s.ScriptsDir)
	}
	creds, err := resolveCredentials(s.CredentialRefs, s.SecretCmd)
	if err != nil {
		return ScriptResult{}, err
	}
	out, err := runScript(s, creds)
	if err != nil {
		return ScriptResult{}, err
	}
	return parseScriptOutput(out, s.Name, s.Account, s.DefaultCurrency)
}

// exitSessionExpired is the script's EX_TEMPFAIL exit: a real session that has expired, with no person
// present to sign in again. It maps to ErrSessionExpired.
const exitSessionExpired = 75

// execScript runs a kind's script with Node from the user's scripts dir -- so it can
// require('./harness.js', the shared session-and-sign-in code -- passing the account's details in the
// environment. NODE_PATH includes the scripts dir and the customer's Playwright (global or
// project-local). Resolved credentials, when present, are handed to the script as a JSON object on
// stdin -- never on the command line or in the environment, where another process could read them --
// so an unattended sign-in can type them; with none, the script falls back to the saved session or a
// headed human sign-in.
func execScript(s Script, creds map[string]string) ([]byte, error) {
	cmd := exec.Command("node", filepath.Join(s.ScriptsDir, s.Kind+".js"))
	cmd.Env = append(os.Environ(),
		"BK_IMPORT_URL="+s.LoginURL,
		"BK_IMPORT_ACCOUNT="+s.Account,
		"BK_IMPORT_CURRENCY="+s.DefaultCurrency,
		"BK_IMPORT_SESSION_FILE="+s.SessionFile,
		"BK_IMPORT_INTERACTIVE="+boolEnv(s.Interactive),
		"BK_IMPORT_RELOGIN="+boolEnv(s.Relogin),
		"NODE_PATH="+nodePath(s.ScriptsDir),
	)
	if s.SnapshotDir != "" {
		cmd.Env = append(cmd.Env, "BK_IMPORT_SNAPSHOT_FILE="+filepath.Join(s.SnapshotDir, s.Kind+".html"))
	}
	if len(s.AccountPath) > 0 {
		pathJSON, err := json.Marshal(s.AccountPath)
		if err != nil {
			return nil, err
		}
		cmd.Env = append(cmd.Env, "BK_IMPORT_ACCOUNT_PATH="+string(pathJSON))
	}
	// A site's date filter (once its panel is open) takes typed dates in "MMM D, YYYY". An explicit
	// range wins; otherwise a relative window is turned into from/to here.
	from, to := s.HistoryFrom, s.HistoryTo
	if from == "" && s.HistoryDays > 0 {
		now := time.Now()
		from = now.AddDate(0, 0, -s.HistoryDays).Format("Jan 2, 2006")
		to = now.Format("Jan 2, 2006")
	}
	if from != "" {
		cmd.Env = append(cmd.Env, "BK_IMPORT_HISTORY_FROM="+from)
		if to != "" {
			cmd.Env = append(cmd.Env, "BK_IMPORT_HISTORY_TO="+to)
		}
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
			return nil, fmt.Errorf("import %s: needs Node and Playwright to drive the browser (node not found)", s.Kind)
		}
		return nil, fmt.Errorf("import %s: %w", s.Kind, err)
	}
	// Read stderr to completion -- routing progress markers to s.Progress and keeping the rest for a
	// failure message -- before waiting on the process, as StderrPipe requires.
	errText := strings.TrimSpace(scanProgress(stderrPipe, s.Progress))
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == exitSessionExpired {
			return nil, fmt.Errorf("import %s: %w", s.Kind, ErrSessionExpired)
		}
		if errText != "" {
			return nil, fmt.Errorf("import %s: %s", s.Kind, errText)
		}
		return nil, fmt.Errorf("import %s: %w", s.Kind, err)
	}
	return stdout.Bytes(), nil
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// nodePath assembles a NODE_PATH so a script can resolve the customer's Playwright: the scripts dir
// itself, any existing NODE_PATH, the project-local node_modules, and the global modules dir when npm
// reports one. It is best-effort; the harness prints a clear message if Playwright is still not found.
func nodePath(scriptsDir string) string {
	var parts []string
	if scriptsDir != "" {
		parts = append(parts, scriptsDir)
	}
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

// scriptRow is one line as the script prints it. The amount is a plain signed decimal string
// ("-62.40"), which the script normalizes; the currency is optional and takes the account's default
// when absent.
type scriptRow struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency,omitempty"`
}

// scriptOutput is what a script prints: the account's rows and, optionally, its current balance. For a
// script that reports rows only, a bare JSON array is also accepted and taken as the rows.
type scriptOutput struct {
	Rows    []scriptRow `json:"rows"`
	Balance string      `json:"balance"`
}

// parseScriptOutput turns the script's JSON into normalized transactions fingerprinted under scope --
// the connector's name, the door the lines entered through -- and, when the script reported one, the
// account's current balance in the account's currency.
func parseScriptOutput(data []byte, scope, account, defaultCurrency string) (ScriptResult, error) {
	data = bytes.TrimSpace(data)
	var out scriptOutput
	if len(data) > 0 && data[0] == '[' { // a bare array: rows only, no balance
		if err := json.Unmarshal(data, &out.Rows); err != nil {
			return ScriptResult{}, fmt.Errorf("import: reading the script's output: %w", err)
		}
	} else if err := json.Unmarshal(data, &out); err != nil {
		return ScriptResult{}, fmt.Errorf("import: reading the script's output: %w", err)
	}

	txs := make([]model.Transaction, 0, len(out.Rows))
	for i, r := range out.Rows {
		date, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return ScriptResult{}, fmt.Errorf("import: row %d: date %q must be YYYY-MM-DD", i+1, r.Date)
		}
		currency := r.Currency
		if currency == "" {
			currency = defaultCurrency
		}
		amount, err := model.NewAmount(r.Amount, currency)
		if err != nil {
			return ScriptResult{}, fmt.Errorf("import: row %d: %w", i+1, err)
		}
		txs = append(txs, model.Transaction{
			Account: account, Date: date, Amount: amount, Description: r.Description,
		})
	}
	Identify(scope, txs)

	res := ScriptResult{Transactions: txs}
	if bal := strings.TrimSpace(out.Balance); bal != "" {
		amount, err := model.NewAmount(bal, defaultCurrency)
		if err != nil {
			return ScriptResult{}, fmt.Errorf("import: reading the balance %q: %w", bal, err)
		}
		res.Balance, res.HasBalance = amount, true
	}
	return res, nil
}
