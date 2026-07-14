package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bank import saves the page it actually read to a snapshot file, so a run that reads nothing (a
// wrong URL, a changed layout, a sign-in that did not land where expected) can be diagnosed by
// looking at the HTML the browser saw instead of guessing. The snapshot's directory is made
// self-ignoring, since the page holds real statement data that must not be committed.
func TestBankScriptWritesASnapshot(t *testing.T) {
	requireBrowserTests(t)
	url := serveFixture(t, "rbc_account.html")
	dir := t.TempDir()

	_, err := execBankScript(Bank{Institution: "rbc", LoginURL: url, DefaultCurrency: "CAD", SnapshotDir: dir}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("running rbc.js: %v", err)
	}

	snap, err := os.ReadFile(filepath.Join(dir, "rbc.html"))
	if err != nil {
		t.Fatalf("no snapshot written: %v", err)
	}
	if !strings.Contains(string(snap), "rbc-transaction-list-table") {
		t.Errorf("snapshot does not hold the page the reader saw")
	}
	if !strings.Contains(string(snap), url) {
		t.Errorf("snapshot does not record the URL it was taken from")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf("snapshot directory is not self-ignoring: %v", err)
	}
}

// A run that fails still leaves a snapshot -- of where it stopped -- which is the whole point of the
// snapshot: a sign-in or navigation that ends nowhere useful is diagnosed from the page it reached,
// not guessed at. Here a login wall with no credentials and no person present fails, and the snapshot
// of that wall must be written before the run gives up.
func TestBankScriptSnapshotsOnFailure(t *testing.T) {
	requireBrowserTests(t)
	url := serveFixture(t, "rbc_login_form.html")
	dir := t.TempDir()

	_, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: url, DefaultCurrency: "CAD",
		SnapshotDir: dir, Interactive: false,
	}, nil)
	skipIfNoBrowser(t, err)
	if err == nil {
		t.Fatal("a login wall with no credentials and no person should fail")
	}
	if _, e := os.Stat(filepath.Join(dir, "rbc.html")); e != nil {
		t.Errorf("no snapshot written on failure: %v", e)
	}
}

// A real run reports its stages through the Progress callback, so the CLI can show a live status
// instead of a silent hang.
func TestBankScriptReportsProgress(t *testing.T) {
	requireBrowserTests(t)
	url := serveFixture(t, "rbc_account.html")

	var stages []string
	_, err := execBankScript(Bank{
		Institution:     "rbc",
		LoginURL:        url,
		DefaultCurrency: "CAD",
		Progress:        func(s string) { stages = append(stages, s) },
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("running rbc.js: %v", err)
	}

	joined := strings.Join(stages, "|")
	if !strings.Contains(joined, "starting a browser") || !strings.Contains(joined, "reading transactions") {
		t.Errorf("progress stages = %v, want at least a start and a read", stages)
	}
}
