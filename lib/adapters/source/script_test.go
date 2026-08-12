package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScriptsDir makes a temp scripts dir holding harness.js and a script per named kind, returning
// its path -- enough for SupportsScript to see the kind without any Node.
func writeScriptsDir(t *testing.T, kinds ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "harness.js"), []byte("module.exports = {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range kinds {
		if err := os.WriteFile(filepath.Join(dir, k+".js"), []byte("// fake\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stubScript swaps the browser runner for a fake for the duration of a test.
func stubScript(t *testing.T, out []byte, err error) {
	t.Helper()
	prev := runScript
	runScript = func(Script, map[string]string) ([]byte, error) { return out, err }
	t.Cleanup(func() { runScript = prev })
}

// A kind is supported iff its <kind>.js exists in the scripts dir; harness is not itself a kind's
// concern, an unknown kind is not supported, and an empty scripts dir supports nothing.
func TestSupportsScript(t *testing.T) {
	dir := writeScriptsDir(t, "fake", "other")
	for _, kind := range []string{"fake", "other"} {
		if !SupportsScript(kind, dir) {
			t.Errorf("SupportsScript(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"nope", ""} {
		if SupportsScript(kind, dir) {
			t.Errorf("SupportsScript(%q) = true, want false", kind)
		}
	}
	if SupportsScript("fake", "") {
		t.Error("SupportsScript with no scripts dir should be false")
	}
}

// Scripts lists the installed kinds -- the *.js basenames minus harness -- for help text.
func TestScriptsListsInstalledKinds(t *testing.T) {
	dir := writeScriptsDir(t, "fake", "other")
	got := Scripts(dir)
	want := map[string]bool{"fake": true, "other": true}
	if len(got) != len(want) {
		t.Fatalf("Scripts = %v, want the two installed kinds", got)
	}
	for _, k := range got {
		if k == "harness" {
			t.Error("Scripts listed harness, which is shared code, not a kind")
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("Scripts = %v, missing %v", got, want)
	}
}

// ReadScript normalizes and fingerprints the script's JSON the same way ReadCSV does, so a scripted
// import and a CSV of one account are interchangeable.
func TestReadScriptNormalizesRows(t *testing.T) {
	dir := writeScriptsDir(t, "fake")
	stubScript(t, []byte(`[
		{"date":"2026-03-01","description":"SHELL GAS","amount":"-62.40"},
		{"date":"2026-03-02","description":"USD CHARGE","amount":"-10.00","currency":"USD"}
	]`), nil)

	res, err := ReadScript(Script{Kind: "fake", ScriptsDir: dir, Account: "Liabilities:Card:Acme", DefaultCurrency: "CAD"})
	if err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	txs := res.Transactions
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}
	if txs[0].Account != "Liabilities:Card:Acme" || txs[0].Amount.String() != "-62.40 CAD" || txs[0].ID == "" {
		t.Errorf("row 0 = %+v", txs[0])
	}
	if txs[1].Amount.String() != "-10.00 USD" { // a row's own currency wins over the default
		t.Errorf("row 1 currency = %q, want the row's own USD", txs[1].Amount.String())
	}
	if res.HasBalance {
		t.Errorf("a bare array has no balance, got %s", res.Balance)
	}
}

// A connector's lines fingerprint under its name, not the account they land in, so re-pointing a
// connector to a new account keeps every id -- a backfill across the re-point still dedupes. Two
// connectors reading an identical line stay distinct.
func TestReadScriptFingerprintsUnderTheConnectorName(t *testing.T) {
	dir := writeScriptsDir(t, "fake")
	rows := []byte(`[{"date":"2026-03-01","description":"MONTHLY FEE","amount":"-4.00"}]`)

	read := func(name, account string) string {
		stubScript(t, rows, nil)
		res, err := ReadScript(Script{Name: name, Kind: "fake", ScriptsDir: dir, Account: account, DefaultCurrency: "CAD"})
		if err != nil {
			t.Fatalf("ReadScript: %v", err)
		}
		return res.Transactions[0].ID
	}

	before := read("acme-chequing", "Assets:Bank:Acme")
	after := read("acme-chequing", "Assets:Consulting:Acme Chequing")
	if before != after {
		t.Errorf("re-pointing the account moved the ID (%q vs %q); a backfill would duplicate history", before, after)
	}
	if other := read("acme-visa", "Assets:Bank:Acme"); other == before {
		t.Error("two connectors produced one ID; the second account's real line would be skipped as a duplicate")
	}
}

// A script may wrap its rows with the account's current balance; ReadScript carries it in the
// account's currency for reconciliation.
func TestReadScriptCarriesTheScrapedBalance(t *testing.T) {
	dir := writeScriptsDir(t, "fake")
	stubScript(t, []byte(`{"rows":[{"date":"2026-03-01","description":"SHELL","amount":"-62.40"}],"balance":"1842.00"}`), nil)

	res, err := ReadScript(Script{Kind: "fake", ScriptsDir: dir, Account: "Assets:Bank:Acme", DefaultCurrency: "CAD"})
	if err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(res.Transactions))
	}
	if !res.HasBalance || res.Balance.String() != "1842.00 CAD" {
		t.Errorf("balance = %s (has=%v), want 1842.00 CAD", res.Balance, res.HasBalance)
	}
}

// A run that fails (a login that did not take, a stubbed script) stops the import with the script's
// message rather than recording an empty statement, and an expired session is surfaced as
// ErrSessionExpired for the caller to turn into a "sign in again" hint.
func TestReadScriptPropagatesRunnerError(t *testing.T) {
	dir := writeScriptsDir(t, "fake")

	stubScript(t, nil, fmt.Errorf("import fake: the script is a stub"))
	if _, err := ReadScript(Script{Kind: "fake", ScriptsDir: dir, Account: "X", DefaultCurrency: "CAD"}); err == nil {
		t.Fatal("a failed run should stop the import")
	}

	stubScript(t, nil, fmt.Errorf("import fake: %w", ErrSessionExpired))
	if _, err := ReadScript(Script{Kind: "fake", ScriptsDir: dir, Account: "X", DefaultCurrency: "CAD"}); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want it to wrap ErrSessionExpired", err)
	}
}

// Resolved credentials reach the runner as field->secret, fetched from their references at import so
// nothing secret is stored; a kind with no script is refused before any of that.
func TestReadScriptResolvesAndPassesCredentials(t *testing.T) {
	dir := writeScriptsDir(t, "fake")

	prevSecret := runSecretCmd
	runSecretCmd = func(name string, args ...string) ([]byte, error) {
		return []byte("secret-for:" + args[len(args)-1]), nil
	}
	t.Cleanup(func() { runSecretCmd = prevSecret })

	var gotCreds map[string]string
	prevRun := runScript
	runScript = func(s Script, creds map[string]string) ([]byte, error) {
		gotCreds = creds
		return []byte("[]"), nil
	}
	t.Cleanup(func() { runScript = prevRun })

	if _, err := ReadScript(Script{
		Kind: "fake", ScriptsDir: dir, DefaultCurrency: "CAD",
		CredentialRefs: map[string]string{"password": "op://Private/Acme/password"},
	}); err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	if gotCreds["password"] != "secret-for:op://Private/Acme/password" {
		t.Errorf("creds passed to runner = %v, want the resolved secret", gotCreds)
	}
}

// A kind with no installed script is refused, naming the dir it looked in, rather than silently
// recording nothing.
func TestReadScriptUnknownKind(t *testing.T) {
	dir := writeScriptsDir(t, "fake")
	_, err := ReadScript(Script{Kind: "missing", ScriptsDir: dir, Account: "X", DefaultCurrency: "CAD"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want a refusal that names the unknown kind", err)
	}
}
