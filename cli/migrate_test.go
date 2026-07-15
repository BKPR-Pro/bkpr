package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// A book from before the door-scoped identity scheme keys its lines to the account they landed
// in. These helpers write such a log directly, because the current binary can no longer produce
// one -- that history is exactly what migrate exists to restate.

var migrateClock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func rawEvent(n int, collection, recordID, action, actor, data string) eventlog.Event {
	return eventlog.Event{
		ID:         fmt.Sprintf("%032x", n),
		Time:       migrateClock.Add(time.Duration(n) * time.Second),
		Collection: collection,
		RecordID:   recordID,
		Action:     action,
		Version:    1,
		Actor:      actor,
		Data:       json.RawMessage(data),
	}
}

func writeLog(t *testing.T, events ...eventlog.Event) {
	t.Helper()
	j, err := eventlog.OpenJSONL(filepath.Join(store.Dir, store.LogFile))
	if err != nil {
		t.Fatalf("OpenJSONL: %v", err)
	}
	defer j.Close()
	for _, e := range events {
		if err := j.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// accountScopedLine is an imported event as the old scheme recorded it: its id fingerprinted
// under the account rather than the door.
func accountScopedLine(n int, actor, account string, date time.Time, amount model.Amount, desc string) eventlog.Event {
	id := source.Fingerprint(account, date, amount, desc) + "-1"
	data, _ := json.Marshal(map[string]any{
		"account": account, "date": date, "amount": amount.String(), "description": desc,
	})
	return rawEvent(n, "transaction", id, "imported", actor, string(data))
}

func doorScopedID(actor string, date time.Time, amount model.Amount, desc string) string {
	door := actor[len("connector:"):] // both prefixes happen to be the same length
	return source.Fingerprint(door, date, amount, desc) + "-1"
}

func logBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(store.Dir, store.LogFile))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return b
}

func eventsByAction(t *testing.T) map[string][]eventlog.Event {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	all, err := s.Log.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	out := map[string][]eventlog.Event{}
	for _, e := range all {
		out[e.Action] = append(out[e.Action], e)
	}
	return out
}

var (
	cad450  = model.Amount{Units: -450, Scale: 2, Commodity: "CAD"}
	cad1200 = model.Amount{Units: 120000, Scale: 2, Commodity: "CAD"}
	mar1    = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mar2    = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
)

// Migrating restates every line's id under the door it entered through, and every fact keyed to
// an old id -- a categorization, a comment, a void, a match and its partner -- follows it. Event
// order, ids, times, and all other data stay exactly as they were.
func TestMigrateRestatesIdsUnderTheirDoors(t *testing.T) {
	bookHere(t)
	coffee := accountScopedLine(1, "connector:bank-main", "Assets:Bank:Chequing", mar1, cad450, "COFFEE SHOP")
	deposit := accountScopedLine(2, "connector:bank-main", "Assets:Bank:Chequing", mar2, cad1200, "CLIENT DEPOSIT")
	writeLog(t,
		coffee,
		deposit,
		rawEvent(3, "transaction", coffee.RecordID, "categorized", "claude", `{"payee":"Coffee","postings":[{"account":"Expenses:Food","amount":"4.50 CAD"}],"why":"test"}`),
		rawEvent(4, "transaction", coffee.RecordID, "commented", "human", `{"comment":"a note"}`),
		rawEvent(5, "transaction", deposit.RecordID, "voided", "claude", `{"why":"wrong"}`),
		rawEvent(6, "transaction", coffee.RecordID, "matched", "human", `{"with":"`+deposit.RecordID+`","paired":true}`),
		rawEvent(7, "rule", "r1", "added", "human", `{"payee":"Coffee","category":"Expenses:Food","match":"COFFEE"}`),
	)

	if err := migrateCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	wantCoffee := doorScopedID("connector:bank-main", mar1, cad450, "COFFEE SHOP")
	wantDeposit := doorScopedID("connector:bank-main", mar2, cad1200, "CLIENT DEPOSIT")
	byAction := eventsByAction(t)
	for action, want := range map[string]string{
		"categorized": wantCoffee, "commented": wantCoffee, "matched": wantCoffee, "voided": wantDeposit,
	} {
		if got := byAction[action][0].RecordID; got != want {
			t.Errorf("%s should follow its line to %s, got %s", action, want, got)
		}
	}
	var m struct {
		With   string `json:"with"`
		Paired bool   `json:"paired"`
	}
	if err := byAction["matched"][0].Decode(&m); err != nil {
		t.Fatalf("decode match: %v", err)
	}
	if m.With != wantDeposit {
		t.Errorf("the match's partner should follow its line to %s, got %s", wantDeposit, m.With)
	}
	if !m.Paired {
		t.Error("re-pointing the partner should not disturb the rest of the match")
	}
	if got := byAction["added"][0]; got.RecordID != "r1" || string(got.Data) != string(rawEvent(7, "", "", "", "", `{"payee":"Coffee","category":"Expenses:Food","match":"COFFEE"}`).Data) {
		t.Error("a rule is not keyed to a line and should be untouched")
	}
	imported := byAction["imported"]
	if imported[0].RecordID != wantCoffee || imported[1].RecordID != wantDeposit {
		t.Errorf("imported ids = %s, %s; want %s, %s", imported[0].RecordID, imported[1].RecordID, wantCoffee, wantDeposit)
	}
	if imported[0].ID != coffee.ID || !imported[0].Time.Equal(coffee.Time) {
		t.Error("migrating renames a line's id, not the event's identity or time")
	}
}

// A connector re-pointed to a new account left the same door's lines under two account-scoped
// ids. Under the door they are one scope, so identical lines take -N suffixes in log order,
// exactly as one import of both would have numbered them.
func TestMigrateMergesARePointedConnectorsHistory(t *testing.T) {
	bookHere(t)
	writeLog(t,
		accountScopedLine(1, "connector:card", "Liabilities:Old", mar1, cad450, "COFFEE SHOP"),
		accountScopedLine(2, "connector:card", "Liabilities:New", mar1, cad450, "COFFEE SHOP"),
	)

	if err := migrateCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	fp := source.Fingerprint("card", mar1, cad450, "COFFEE SHOP")
	imported := eventsByAction(t)["imported"]
	if imported[0].RecordID != fp+"-1" || imported[1].RecordID != fp+"-2" {
		t.Errorf("ids = %s, %s; want %s-1, %s-2", imported[0].RecordID, imported[1].RecordID, fp, fp)
	}
}

// Without -confirm nothing is touched: migrate only says what would move.
func TestMigrateIsADryRunWithoutConfirm(t *testing.T) {
	bookHere(t)
	line := accountScopedLine(1, "connector:bank-main", "Assets:Bank:Chequing", mar1, cad450, "COFFEE SHOP")
	writeLog(t, line, rawEvent(2, "transaction", line.RecordID, "categorized", "claude", `{"payee":"Coffee"}`))
	before := logBytes(t)

	if err := migrateCmd(nil); err != nil {
		t.Fatalf("migrate dry run: %v", err)
	}
	if string(logBytes(t)) != string(before) {
		t.Error("a dry run rewrote the log")
	}
}

// A migrated book is already keyed to its doors, so migrating again moves nothing.
func TestMigrateIsIdempotent(t *testing.T) {
	bookHere(t)
	line := accountScopedLine(1, "connector:bank-main", "Assets:Bank:Chequing", mar1, cad450, "COFFEE SHOP")
	writeLog(t, line, rawEvent(2, "transaction", line.RecordID, "categorized", "claude", `{"payee":"Coffee"}`))

	if err := migrateCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	after := logBytes(t)
	if err := migrateCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if string(logBytes(t)) != string(after) {
		t.Error("migrating a migrated book changed it")
	}
}

// An imported line whose actor names no door cannot be given a scope; refusing beats guessing,
// and a refusal must leave the log untouched.
func TestMigrateRefusesALineWithNoDoor(t *testing.T) {
	bookHere(t)
	writeLog(t, accountScopedLine(1, "human", "Assets:Bank:Chequing", mar1, cad450, "COFFEE SHOP"))
	before := logBytes(t)

	if err := migrateCmd([]string{"-confirm"}); err == nil {
		t.Fatal("migrate should refuse an imported line with no door")
	}
	if string(logBytes(t)) != string(before) {
		t.Error("a refused migrate rewrote the log")
	}
}

// A settlement names the bank line that paid it, so that reference follows the line too. The
// accrual's own id is not a statement line and stays put.
func TestMigrateRewritesASettlementsPayingLine(t *testing.T) {
	bookHere(t)
	line := accountScopedLine(1, "connector:bank-main", "Assets:Bank:Chequing", mar2, cad1200, "CLIENT DEPOSIT")
	writeLog(t,
		line,
		rawEvent(2, "invoice", "inv1", "raised", "human", `{"date":"2026-03-01T00:00:00Z","party":"Client","amount":"1200.00 CAD","category":"Income:Consulting","account":"Assets:Receivable"}`),
		rawEvent(3, "invoice", "inv1", "settled", "human", `{"tx":"`+line.RecordID+`"}`),
	)

	if err := migrateCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	settled := eventsByAction(t)["settled"][0]
	if settled.RecordID != "inv1" {
		t.Errorf("an accrual's id should stay put, got %s", settled.RecordID)
	}
	var d struct {
		Tx string `json:"tx"`
	}
	if err := settled.Decode(&d); err != nil {
		t.Fatalf("decode settled: %v", err)
	}
	if want := doorScopedID("connector:bank-main", mar2, cad1200, "CLIENT DEPOSIT"); d.Tx != want {
		t.Errorf("the settlement's paying line should follow to %s, got %s", want, d.Tx)
	}
}

// The point of the whole exercise: after migrating, re-reading a window the book already holds
// is a no-op again. The statement lands through the same door with the same content, so every
// line dedupes against the restated ids.
func TestMigrateThenOverlappingImportDedupes(t *testing.T) {
	bookHere(t)
	csv := "Date,Description,Amount\n2026-03-01,COFFEE SHOP,-4.50\n2026-03-02,CLIENT DEPOSIT,1200.00\n"
	if err := os.WriteFile("lines.csv", []byte(csv), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	writeLog(t,
		accountScopedLine(1, "statement:lines.csv", "Assets:Bank:Chequing", mar1, cad450, "COFFEE SHOP"),
		accountScopedLine(2, "statement:lines.csv", "Assets:Bank:Chequing", mar2, cad1200, "CLIENT DEPOSIT"),
	)

	if err := migrateCmd([]string{"-confirm"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := importCmd([]string{"lines.csv", "-account", "Assets:Bank:Chequing", "-currency", "CAD", "-amount", "Amount"}); err != nil {
		t.Fatalf("re-import: %v", err)
	}

	if imported := eventsByAction(t)["imported"]; len(imported) != 2 {
		t.Fatalf("the overlap re-landed: %d imported lines, want 2", len(imported))
	}
}
