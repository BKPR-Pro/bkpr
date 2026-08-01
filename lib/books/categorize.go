package books

import (
	"encoding/json"
	"fmt"
	"time"

	"bkpr.pro/bkpr/lib/eventlog"
	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/rules"
)

// ActionCategorized records that a person or an agent asserted the postings for one transaction.
// It is keyed by the transaction's fingerprint and overrides whatever the rules would have said
// for that one line. It never generalizes, because the attribution is real-world context the
// description does not contain.
const ActionCategorized = "categorized"

type categorizedData struct {
	Payee         string          `json:"payee"`
	Invoice       string          `json:"invoice,omitempty"` // the invoice or bill number, the ledger (code)
	Postings      []model.Posting `json:"postings"`
	Source        string          `json:"source,omitempty"`        // where the elided leg lands, when routed off the tx account
	Gain          string          `json:"gain,omitempty"`          // set on a sale: the account its capital gain lands in
	BlockComments []string        `json:"blockComments,omitempty"` // standalone notes carried from a ledger file
	Why           string          `json:"why,omitempty"`

	// ExplicitPosts marks an assertion whose legs the caller spelled out (categorize's -post form, a
	// ledger file's own entry) rather than derived from a category. Spelled legs are the caller's
	// arithmetic, so a rule's tax overlay never restates them; a category-form assertion, where the
	// tool computed the one leg, stays open to it. Absent on events written before the overlay
	// existed, which reads as false: their shape (a single derived leg) is the category form.
	ExplicitPosts bool `json:"explicitPosts,omitempty"`
}

// Categorize asserts the postings for one imported transaction.
//
// The line must exist, so the fingerprint cannot orphan, and the postings must account for the
// whole line, so the books stay balanced. Both are checked here rather than left for the render:
// the books are the artifact, and a bad assertion should be refused at the moment it is made.
func Categorize(log *eventlog.Log, actor, why, txID, invoice, payee, source string, postings []model.Posting) error {
	return categorizeChecked(log, actor, why, txID, invoice, payee, source, postings, false)
}

// CategorizePosts is Categorize for postings the caller spelled out leg by leg — categorize's
// -post form. The spelled legs are the caller's own arithmetic (a hand-made split, or a deliberate
// no-split), so the assertion outranks a rule's tax overlay where a category-form assertion, whose
// one leg the tool derived, would still take it.
func CategorizePosts(log *eventlog.Log, actor, why, txID, invoice, payee, source string, postings []model.Posting) error {
	return categorizeChecked(log, actor, why, txID, invoice, payee, source, postings, true)
}

func categorizeChecked(log *eventlog.Log, actor, why, txID, invoice, payee, source string, postings []model.Posting, explicitPosts bool) error {
	tx, err := Transaction(log, txID)
	if err != nil {
		return err
	}

	entry := model.Entry{Payee: payee, Postings: postings, Source: source}
	if !entry.Balances(tx) {
		return fmt.Errorf("books: the postings do not account for %s", tx.Amount.Negate())
	}

	// tx.ID, not txID: the caller may have quoted a prefix, and the assertion must key to the line.
	return assertCategorized(log, actor, why, tx.ID, invoice, payee, source, postings, nil, explicitPosts)
}

// assertCategorized records one categorization event, keyed by the transaction's fingerprint. It
// is the shared tail of a hand correction and a carried-in categorization: both are the same fact
// about one line, an assertion that overrides whatever the rules would have said.
func assertCategorized(log *eventlog.Log, actor, why, txID, invoice, payee, source string, postings []model.Posting, blockComments []string, explicitPosts bool) error {
	data, err := json.Marshal(categorizedData{Payee: payee, Invoice: invoice, Postings: postings, Source: source, BlockComments: blockComments, Why: why, ExplicitPosts: explicitPosts})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionCategorized,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// CarryCategorizations records the categorization each transaction arrived with, so a source that
// already names its accounts — a ledger file, not a raw statement — needs no manual categorize per
// line. The transactions must already be imported, and txs[i] is categorized by entries[i].
//
// An entry with no postings, or one that does not account for its line (a mixed-commodity
// placeholder the books cannot post), is left to the rules and counted as skipped rather than
// asserted: the import carries what it faithfully can and never writes a broken entry. The carried
// facts are ordinary assertions, so a later human correction still wins over them. A file spells
// its own legs, so the carry records them as spelled: a rule's tax overlay never restates a carried
// entry, and a re-imported historical book keeps its splits exactly as written.
func CarryCategorizations(log *eventlog.Log, actor, why string, txs []model.Transaction, entries []model.Entry) (carried, skipped int, err error) {
	if len(txs) != len(entries) {
		return 0, 0, fmt.Errorf("books: %d transactions but %d categorizations", len(txs), len(entries))
	}
	for i, tx := range txs {
		entry := entries[i]
		if len(entry.Postings) == 0 || !entry.Balances(tx) {
			skipped++
			continue
		}
		if err := assertCategorized(log, actor, why, tx.ID, entry.Invoice, entry.Payee, entry.Source, entry.Postings, entry.BlockComments, true); err != nil {
			return carried, skipped, err
		}
		carried++
	}
	return carried, skipped, nil
}

// Ledger folds the whole log into transactions and their final entries, in date order, on the cash
// basis. Each line is categorized by the rules, then overridden by the latest human or model
// assertion for that specific line. This is the pipeline the table and the ledger artifact both
// render, and the basis the tool was born on: every line is money that actually moved.
func Ledger(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
	return LedgerBasis(log, CashBasis)
}

// Basis is the lens the books are read through. Cash records only money that moved; accrual also
// books the revenue that was earned before it. Both are folds over the one log: the basis is chosen
// at read time, never stored, so a book of statements can be read either way and switched between
// them without rewriting anything.
type Basis string

const (
	CashBasis    Basis = "cash"
	AccrualBasis Basis = "accrual"
)

// LedgerBasis folds the log through the chosen lens. On the cash basis it is Ledger: only money that
// moved. On the accrual basis it also books every open invoice and bill as its own line and lets a
// settling bank line clear the receivable or payable it raised. The basis is a read-time choice over
// one log, so the same books can be read either way, and a set of books can switch between them
// without any rewrite: the accrual lines simply appear or fall away.
func LedgerBasis(log *eventlog.Log, basis Basis) ([]model.Transaction, []model.Entry, error) {
	return LedgerBasisSince(log, basis, time.Time{})
}

// LedgerBasisSince is LedgerBasis with an effective date for the switch to accrual: on the accrual
// basis only invoices and bills dated on or after since are booked, so a book can turn on accrual
// mid-year without retroactively accruing everything before it. A zero since books all of them. The
// date is a read-time argument, never stored, so the seam it creates is a fact about how you are
// reading the log, not a change to the log. An accrual dated before since is treated exactly as cash:
// its payment books as ordinary income or expense when it lands.
func LedgerBasisSince(log *eventlog.Log, basis Basis, since time.Time) ([]model.Transaction, []model.Entry, error) {
	txs, entries, err := cashLedger(log)
	if err != nil {
		return nil, nil, err
	}
	if basis == AccrualBasis {
		return overlayAccruals(log, txs, entries, since)
	}
	return txs, entries, nil
}

// cashLedger folds the log into the lines the bank reported and their final entries, in date order.
func cashLedger(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
	txs, entries, err := categorized(log)
	if err != nil {
		return nil, nil, err
	}

	// A sale asserts only which shares left; the cost base they carry, and so the gain, is folded
	// from the account's history here rather than stored on the assertion, under each account's
	// cost-basis policy.
	policies, err := Policies(log)
	if err != nil {
		return nil, nil, err
	}
	if err := resolveDisposals(txs, entries, policies.For); err != nil {
		return nil, nil, err
	}

	// A manual match overrides the automatic pairing; it is folded here and applied by suppressed.
	overrides, err := matches(log)
	if err != nil {
		return nil, nil, err
	}

	// The duplicate sighting of an internal transfer must not book a second entry, so it is dropped
	// from the books entirely rather than rendered. Every source account is one you own, which is what
	// lets a transfer between two of them be told from a coincidental deposit.
	owned := make(map[string]bool, len(txs))
	for _, tx := range txs {
		owned[tx.Account] = true
	}
	// A line categorized by hand or carried from a ledger file is an assertion, not a guess. The loose
	// transfer fold, which pairs unnamed sightings on size alone, must not dissolve two such assertions;
	// the assertions say what each line is. Their recency also picks the surviving side of a same-day
	// pair: the asserted side speaks for the movement.
	asserted, err := assertions(log)
	if err != nil {
		return nil, nil, err
	}
	assertedAt := make(map[string]int, len(asserted))
	for id, a := range asserted {
		assertedAt[id] = a.at
	}
	dup := suppressed(txs, entries, overrides, owned, assertedAt)
	keptTxs := make([]model.Transaction, 0, len(txs))
	keptEntries := make([]model.Entry, 0, len(entries))
	for i, tx := range txs {
		if dup[tx.ID] {
			continue
		}
		keptTxs = append(keptTxs, tx)
		keptEntries = append(keptEntries, entries[i])
	}
	return keptTxs, keptEntries, nil
}

// categorized folds the log into every transaction and the entry it currently carries, in date
// order, before disposals are priced and transfers suppressed. Each line is categorized by the
// rules, then overridden by the latest human or model assertion for that specific line. A rule's
// tax is orthogonal to that override — "this vendor's prices include tax" is a fact about the
// vendor, the category a fact about the line — so an opted-in rule still lays its tax onto the
// asserted entry, unless the caller spelled the legs out. It is the shared front half of Ledger,
// reused to validate a sale before it is recorded.
func categorized(log *eventlog.Log) ([]model.Transaction, []model.Entry, error) {
	set, err := Rules(log)
	if err != nil {
		return nil, nil, err
	}
	engine, err := rules.New(set)
	if err != nil {
		return nil, nil, err
	}

	txs, err := Transactions(log)
	if err != nil {
		return nil, nil, err
	}

	asserted, err := assertions(log)
	if err != nil {
		return nil, nil, err
	}

	entries := make([]model.Entry, len(txs))
	for i, tx := range txs {
		if a, ok := asserted[tx.ID]; ok {
			entries[i] = a.entry
			if !a.explicitPosts {
				entries[i] = engine.OverlayTax(tx, a.entry)
			}
			continue
		}
		entries[i] = engine.Apply(tx)
	}
	return txs, entries, nil
}

// assertion is one line's latest categorization fact as the fold reads it: the entry it asserted,
// whether its legs were spelled out by the caller (which keeps the tax overlay off them), and where
// in the log it sits -- its recency, which the transfer fold uses to pick the surviving side of a
// same-day pair.
type assertion struct {
	entry         model.Entry
	explicitPosts bool
	at            int
}

// assertions folds the categorized events into the current assertion per transaction. A later
// event about the same line replaces an earlier one, so the map simply takes each in log order.
func assertions(log *eventlog.Log) (map[string]assertion, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	out := map[string]assertion{}
	for i, e := range events {
		if e.Collection != CollectionTransaction || e.Action != ActionCategorized {
			continue
		}
		var data categorizedData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out[e.RecordID] = assertion{
			entry:         model.Entry{Payee: data.Payee, Invoice: data.Invoice, Postings: data.Postings, Source: data.Source, Gain: data.Gain, BlockComments: data.BlockComments},
			explicitPosts: data.ExplicitPosts,
			at:            i,
		}
	}
	return out, nil
}
