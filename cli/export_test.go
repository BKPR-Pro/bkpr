package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/adapters/rentapp"
	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
	"github.com/BKPR-Pro/bkpr/lib/rules"
)

// capture records what the mock rent app was asked to do, so a test can assert the export sent the
// right lease, amount, and idempotency key without a live call.
type capture struct{ path, auth, idem, amount string }

func mockRent(t *testing.T, cap *capture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.idem = r.Header.Get("Idempotency-Key")
		cap.amount = r.PostForm.Get("amount")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"transaction":{"id":"rr_1","lease_id":"31","amount_cents":168000}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rentDepositLog is a book with one rent deposit whose rule attributes it to a property and names
// the lease it should be exported against.
func rentDepositLog(t *testing.T) *eventlog.Log {
	t.Helper()
	log := eventlog.New(eventlog.NewMemory())
	r := rules.Rule{
		Match: "taylor", Category: "Income:Real Estate:Rent:22 Cedar Street",
		Metadata: map[string]string{"rentapp.lease": "31"},
	}
	if err := books.AddRule(log, "human", r, ""); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if _, err := books.Import(log, "statement:march", []model.Transaction{{
		ID: "dep1", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: 168000, Scale: 2, Commodity: "CAD"}, Description: "E-TRANSFER FROM JAMIE TAYLOR",
	}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	return log
}

// The heart of the output direction: a categorized rent deposit is recorded against its lease, with
// the deposit's fingerprint as the idempotency key, and marked exported so it is not sent again.
func TestExportRecordsARentDepositAgainstItsLease(t *testing.T) {
	cap := &capture{}
	srv := mockRent(t, cap)
	log := rentDepositLog(t)

	res, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", true)
	if err != nil {
		t.Fatalf("exportRent: %v", err)
	}

	if len(res.Recorded) != 1 || res.Recorded[0].lease != "31" {
		t.Fatalf("recorded = %+v, want one against lease 31", res.Recorded)
	}
	if cap.path != "/rentroll/record/31.json" {
		t.Errorf("path = %q, want the lease's record endpoint", cap.path)
	}
	if cap.amount != "1680.00" {
		t.Errorf("amount = %q, want 1680.00", cap.amount)
	}
	if cap.idem != "dep1" {
		t.Errorf("idempotency key = %q, want the deposit fingerprint", cap.idem)
	}
	if cap.auth != "Bearer tok" {
		t.Errorf("auth = %q, want the bearer token", cap.auth)
	}
	if exported, _ := books.Exported(log); !exported["dep1"] {
		t.Error("dep1 was not marked exported")
	}
}

// Re-running the export sends nothing: the deposit is already recorded, so no second POST is made.
func TestExportSkipsADepositAlreadySent(t *testing.T) {
	cap := &capture{}
	srv := mockRent(t, cap)
	log := rentDepositLog(t)
	client := rentapp.New(srv.URL, "tok")

	exportRent(log, client, "rent", true)

	cap.path = ""
	res, err := exportRent(log, client, "rent", true)
	if err != nil {
		t.Fatalf("second exportRent: %v", err)
	}
	if len(res.Recorded) != 0 {
		t.Errorf("recorded %d on the second run, want 0", len(res.Recorded))
	}
	if cap.path != "" {
		t.Errorf("a second POST was made to %q; the export is not idempotent", cap.path)
	}
}

// A dry run plans the export but sends nothing and records nothing, so a person can read what will
// happen before it does.
func TestExportDryRunSendsNothing(t *testing.T) {
	cap := &capture{}
	srv := mockRent(t, cap)
	log := rentDepositLog(t)

	res, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", false)
	if err != nil {
		t.Fatalf("exportRent: %v", err)
	}
	if len(res.Planned) != 1 {
		t.Errorf("planned %d, want 1", len(res.Planned))
	}
	if cap.path != "" {
		t.Errorf("a dry run made a POST to %q", cap.path)
	}
	if exported, _ := books.Exported(log); exported["dep1"] {
		t.Error("a dry run marked dep1 exported")
	}
}

// A deposit no rule attributed to a lease is left alone: without a lease there is nothing to record
// it against, so it is never exported and never guessed.
func TestExportLeavesADepositWithoutALease(t *testing.T) {
	cap := &capture{}
	srv := mockRent(t, cap)
	log := eventlog.New(eventlog.NewMemory())
	if _, err := books.Import(log, "statement:march", []model.Transaction{{
		ID: "gas1", Account: "Assets:Bank:Chequing", Date: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
		Amount: model.Amount{Units: -6240, Scale: 2, Commodity: "CAD"}, Description: "SHELL GAS #123",
	}}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	res, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", true)
	if err != nil {
		t.Fatalf("exportRent: %v", err)
	}
	if len(res.Recorded) != 0 || cap.path != "" {
		t.Errorf("exported a deposit with no lease: recorded=%+v path=%q", res.Recorded, cap.path)
	}
}
