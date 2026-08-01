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
// right lease, amount, idempotency key, and reference without a live call.
type capture struct{ path, auth, idem, amount, reference string }

func mockRent(t *testing.T, cap *capture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.idem = r.Header.Get("Idempotency-Key")
		cap.amount = r.PostForm.Get("amount")
		cap.reference = r.PostForm.Get("reference")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"transaction":{"id":"rr_1","lease_id":"31","amount_cents":168000}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mockRentReplyingConflict simulates the app's own UI having already recorded the period: it refuses
// with 409 regardless of what is sent.
func mockRentReplyingConflict(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "already recorded", http.StatusConflict)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mockRentFailing simulates a genuine failure (a 500), distinct from an already-recorded period.
func mockRentFailing(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
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

// rentDepositLogWithInvoice is rentDepositLog's deposit, but raised and settled as a numbered
// invoice first, the way accrual-basis rent is booked before its cash arrives.
func rentDepositLogWithInvoice(t *testing.T, number string) *eventlog.Log {
	t.Helper()
	log := rentDepositLog(t)
	inv, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Party: "Commercial Tenant",
		Amount:   model.Amount{Units: 168000, Scale: 2, Commodity: "CAD"},
		Category: "Income:Real Estate:Rent:22 Cedar Street", Number: number,
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if err := books.SettleInvoice(log, "human", inv.ID, "dep1"); err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}
	return log
}

// A deposit that settles a numbered invoice carries that number through as the reference, so the
// app's rent description can cite it.
func TestExportSendsTheInvoiceNumberAsReference(t *testing.T) {
	cap := &capture{}
	srv := mockRent(t, cap)
	log := rentDepositLogWithInvoice(t, "2086")

	if _, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", true); err != nil {
		t.Fatalf("exportRent: %v", err)
	}
	if cap.reference != "2086" {
		t.Errorf("reference = %q, want 2086", cap.reference)
	}
}

// The ordinary case -- a rent line categorized by rule, no invoice behind it -- sends no reference,
// and the export is unaffected by an invoice lookup existing at all.
func TestExportSendsNoReferenceWithoutAnInvoice(t *testing.T) {
	cap := &capture{}
	srv := mockRent(t, cap)
	log := rentDepositLog(t)

	if _, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", true); err != nil {
		t.Fatalf("exportRent: %v", err)
	}
	if cap.reference != "" {
		t.Errorf("reference = %q, want none", cap.reference)
	}
}

// A 409 means the app's own UI already recorded this period -- the other valid path having gotten
// there first. It is reported as skipped, not failed, and does not fail the overall command.
func TestExportSkipsAPeriodAlreadyRecordedInTheApp(t *testing.T) {
	srv := mockRentReplyingConflict(t)
	log := rentDepositLog(t)

	res, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", true)
	if err != nil {
		t.Fatalf("exportRent: %v", err)
	}
	if len(res.Skipped) != 1 || len(res.Failed) != 0 || len(res.Recorded) != 0 {
		t.Fatalf("res = %+v, want one skipped and nothing failed or recorded", res)
	}
}

// A genuine failure (network error, 500, wrong token) is still reported as failed and still fails
// the command, unlike a 409.
func TestExportFailsOnAGenuineError(t *testing.T) {
	srv := mockRentFailing(t)
	log := rentDepositLog(t)

	res, err := exportRent(log, rentapp.New(srv.URL, "tok"), "rent", true)
	if err == nil {
		t.Fatal("exportRent: want an error on a genuine failure")
	}
	if len(res.Failed) != 1 || len(res.Skipped) != 0 || len(res.Recorded) != 0 {
		t.Fatalf("res = %+v, want one failed and nothing skipped or recorded", res)
	}
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
