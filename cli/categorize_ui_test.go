package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/model"
)

func dayAgo(months int) string {
	return time.Now().AddDate(0, -months, 0).Format("2006-01-02")
}

func importDated(t *testing.T, id, date, description string, cents int64) {
	t.Helper()
	log, closeLog, err := open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer closeLog()
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}
	if _, err := books.Import(log, "statement:test", []model.Transaction{{
		ID: id, Account: "Assets:Bank:Chequing", Date: parsed,
		Amount: model.Amount{Units: cents, Scale: 2, Commodity: "CAD"}, Description: description,
	}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
}

// extractCategorizeUIData pulls the DATA = {...}; JSON literal the template embeds, so a test can
// assert on the page's actual data without parsing HTML.
func extractCategorizeUIData(t *testing.T, page string) categorizeUIPage {
	t.Helper()
	start := strings.Index(page, "const DATA = ")
	if start == -1 {
		t.Fatalf("page has no DATA literal:\n%s", page)
	}
	start += len("const DATA = ")
	end := strings.Index(page[start:], ";\n")
	if end == -1 {
		t.Fatalf("DATA literal has no terminator")
	}
	var data categorizeUIPage
	if err := json.Unmarshal([]byte(page[start:start+end]), &data); err != nil {
		t.Fatalf("DATA literal is not JSON: %v\n%s", err, page[start:start+end])
	}
	return data
}

func TestCategorizeUIListsOnlyLinesInTheWindow(t *testing.T) {
	bookHere(t)
	importDated(t, "recent", dayAgo(1), "RECENT LINE", -1000)
	importDated(t, "old", dayAgo(6), "OLD LINE", -2000)

	out, err := withPipedStdout(t, func() error {
		return categorizeUIGenerate([]string{"-from", dayAgo(2)})
	})
	if err != nil {
		t.Fatalf("categorizeUIGenerate: %v", err)
	}
	data := extractCategorizeUIData(t, out)
	if len(data.Txns) != 1 || data.Txns[0].Fingerprint != "recent" {
		t.Fatalf("txns = %+v, want just the recent line", data.Txns)
	}
}

func TestCategorizeUIDefaultsToFourMonthsBack(t *testing.T) {
	bookHere(t)
	importDated(t, "recent", dayAgo(1), "RECENT LINE", -1000)
	importDated(t, "old", dayAgo(6), "OLD LINE", -2000)

	out, err := withPipedStdout(t, func() error { return categorizeUIGenerate(nil) })
	if err != nil {
		t.Fatalf("categorizeUIGenerate: %v", err)
	}
	data := extractCategorizeUIData(t, out)
	if len(data.Txns) != 1 || data.Txns[0].Fingerprint != "recent" {
		t.Fatalf("txns = %+v, want only the line inside the default 4-month window", data.Txns)
	}
}

func TestCategorizeUIRowsCarryTheirCurrentCategory(t *testing.T) {
	bookHere(t)
	importDated(t, "tx1", dayAgo(1), "THING", -1000)
	if err := categorize([]string{"tx1", "-category", "Expenses:Meals"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	out, err := withPipedStdout(t, func() error { return categorizeUIGenerate(nil) })
	if err != nil {
		t.Fatalf("categorizeUIGenerate: %v", err)
	}
	data := extractCategorizeUIData(t, out)
	if len(data.Txns) != 1 || data.Txns[0].Category != "Expenses:Meals" {
		t.Fatalf("txns = %+v, want tx1 categorized to Expenses:Meals", data.Txns)
	}
}

func TestCategorizeUICategoriesIncludeAccountsOutsideTheWindow(t *testing.T) {
	bookHere(t)
	importDated(t, "old", dayAgo(8), "OLD LINE", -1000)
	if err := categorize([]string{"old", "-category", "Expenses:Ancient"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	importDated(t, "recent", dayAgo(1), "RECENT LINE", -2000)

	out, err := withPipedStdout(t, func() error {
		return categorizeUIGenerate([]string{"-from", dayAgo(2)})
	})
	if err != nil {
		t.Fatalf("categorizeUIGenerate: %v", err)
	}
	data := extractCategorizeUIData(t, out)
	found := false
	for _, c := range data.Categories {
		if c == "Expenses:Ancient" {
			found = true
		}
	}
	if !found {
		t.Errorf("categories = %v, want Expenses:Ancient even though its line is outside the window", data.Categories)
	}
}

// The page's account column is buildRegister's own SourceAccount, not the raw import account, so a
// -source-routed line reads the same account here as it does in `bkpr register`.
func TestCategorizeUIAccountReflectsSourceRouting(t *testing.T) {
	bookHere(t)
	importDated(t, "tx1", dayAgo(1), "KENT BUILDING SUPPLIES", -10000)
	if err := categorize([]string{"tx1", "-category", "Expenses:Materials", "-source", "Assets:Bank:Chequing:9 Schoodic Street"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}

	out, err := withPipedStdout(t, func() error { return categorizeUIGenerate(nil) })
	if err != nil {
		t.Fatalf("categorizeUIGenerate: %v", err)
	}
	data := extractCategorizeUIData(t, out)
	if len(data.Txns) != 1 || data.Txns[0].Account != "Assets:Bank:Chequing:9 Schoodic Street" {
		t.Fatalf("txns = %+v, want the routed source account", data.Txns)
	}
}

func TestCategorizeUIRowsSortNewestFirst(t *testing.T) {
	bookHere(t)
	importDated(t, "early", dayAgo(3), "EARLY", -1000)
	importDated(t, "late", dayAgo(1), "LATE", -1000)

	out, err := withPipedStdout(t, func() error { return categorizeUIGenerate(nil) })
	if err != nil {
		t.Fatalf("categorizeUIGenerate: %v", err)
	}
	data := extractCategorizeUIData(t, out)
	if len(data.Txns) != 2 || data.Txns[0].Fingerprint != "late" || data.Txns[1].Fingerprint != "early" {
		t.Fatalf("txns = %+v, want late before early", data.Txns)
	}
}

func writeChangesFile(t *testing.T, changes []categorizeUIChange) string {
	t.Helper()
	data, err := json.Marshal(changes)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "changes.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func categoryOf(t *testing.T, id string) string {
	t.Helper()
	log, closeLog, err := openReader()
	if err != nil {
		t.Fatalf("openReader: %v", err)
	}
	defer closeLog()
	txs, entries, err := books.Ledger(log)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	for i, tx := range txs {
		if tx.ID == id {
			if len(entries[i].Postings) == 0 {
				return ""
			}
			return entries[i].Postings[0].Account
		}
	}
	t.Fatalf("no line %q", id)
	return ""
}

func TestCategorizeUIApplyAssertsTheNewCategory(t *testing.T) {
	bookHere(t)
	importDated(t, "tx1", dayAgo(1), "THING", -1000)
	if err := categorize([]string{"tx1", "-category", "Expenses:Old"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	path := writeChangesFile(t, []categorizeUIChange{{Fingerprint: "tx1", NewCategory: "Expenses:New"}})

	if _, err := withPipedStdout(t, func() error { return categorizeUIApply([]string{path}) }); err != nil {
		t.Fatalf("categorizeUIApply: %v", err)
	}
	if got := categoryOf(t, "tx1"); got != "Expenses:New" {
		t.Errorf("category = %q, want Expenses:New", got)
	}
}

func TestCategorizeUIApplyRecordsTheGivenActor(t *testing.T) {
	bookHere(t)
	importDated(t, "tx1", dayAgo(1), "THING", -1000)
	path := writeChangesFile(t, []categorizeUIChange{{Fingerprint: "tx1", NewCategory: "Expenses:New"}})

	if _, err := withPipedStdout(t, func() error {
		return categorizeUIApply([]string{"-actor", "model:claude", path})
	}); err != nil {
		t.Fatalf("categorizeUIApply: %v", err)
	}
	if got := actorOf(t, "transaction", "categorized"); got != "model:claude" {
		t.Errorf("actor = %q, want model:claude", got)
	}
}

func TestCategorizeUIApplySkipsATaxSplitLine(t *testing.T) {
	bookHere(t)
	importDated(t, "tx1", dayAgo(1), "THING", -11500)
	if err := categorize([]string{"tx1", "-category", "Expenses:Materials", "-tax-rate", "15%", "-tax-account", "Assets:HST ITC"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	path := writeChangesFile(t, []categorizeUIChange{{Fingerprint: "tx1", NewCategory: "Expenses:Other"}})

	out, err := withPipedStdout(t, func() error { return categorizeUIApply([]string{path}) })
	if err != nil {
		t.Fatalf("categorizeUIApply: %v", err)
	}
	if got := categoryOf(t, "tx1"); got != "Expenses:Materials" {
		t.Errorf("category = %q, want the tax-split line left untouched", got)
	}
	if !strings.Contains(out, "skipped 1") {
		t.Errorf("output = %q, want it to report 1 skipped", out)
	}
}

func TestCategorizeUIApplyIgnoresChangesBackToTheSameCategory(t *testing.T) {
	bookHere(t)
	importDated(t, "tx1", dayAgo(1), "THING", -1000)
	if err := categorize([]string{"tx1", "-category", "Expenses:Same"}); err != nil {
		t.Fatalf("categorize: %v", err)
	}
	path := writeChangesFile(t, []categorizeUIChange{{Fingerprint: "tx1", NewCategory: ""}})

	out, err := withPipedStdout(t, func() error { return categorizeUIApply([]string{path}) })
	if err != nil {
		t.Fatalf("categorizeUIApply: %v", err)
	}
	if !strings.Contains(out, "applied 0") {
		t.Errorf("output = %q, want a blank new_category to apply nothing", out)
	}
}
