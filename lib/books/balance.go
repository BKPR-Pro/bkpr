package books

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"bkpr.pro/bkpr/lib/eventlog"
	"bkpr.pro/bkpr/lib/model"
)

// CollectionBalance is keyed by an account path. Each event asserts what the bank said an account's
// balance was on a date -- ground truth to reconcile the books against, recorded on every import so
// the check runs itself. It books nothing; it is not a transaction. It only lets a fold ask whether
// the books, folded to that date, agree with the bank to the penny.
const CollectionBalance = "balance"

// ActionAsserted records a bank-stated balance for an account on a date.
const ActionAsserted = "asserted"

// ActionRetired closes an account's reconciliation: it is finished, and reconcile should stop
// reporting it. An account gets here by being emptied rather than by being wrong -- a connector
// re-pointed away from it, or its lines collapsed onto the account they belonged to -- and until now
// it could not be said. The first assertion for an account derives its opening balance, so the account
// matches by construction however little it holds, and asserting zero does not retire it: the first
// anchor stays first, its derived offset stays, and the zero reads as a delta the size of the offset.
// Retiring drops the assertions before it; a balance recorded afterwards starts the account over.
const ActionRetired = "retired"

type balanceData struct {
	Date   time.Time    `json:"date"`
	Amount model.Amount `json:"amount"`
}

// AssertBalance records what the bank said an account held on a date. Re-importing a statement re-reads
// the same balance and simply appends another assertion; the fold takes them in date order, so a repeat
// changes nothing meaningful.
func AssertBalance(log *eventlog.Log, actor, account string, date time.Time, amount model.Amount) error {
	if account == "" {
		return fmt.Errorf("books: a balance needs an account")
	}
	data, err := json.Marshal(balanceData{Date: date, Amount: amount})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionBalance, RecordID: account, Action: ActionAsserted,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// RetireBalance closes an account's reconciliation. Nothing is deleted: the account's transactions and
// its recorded balances stay in the log, and a later AssertBalance re-anchors it from that point.
func RetireBalance(log *eventlog.Log, actor, account string) error {
	if account == "" {
		return fmt.Errorf("books: retiring a balance needs an account")
	}
	_, err := log.Track(eventlog.Event{
		Collection: CollectionBalance, RecordID: account, Action: ActionRetired,
		Version: version, Actor: actor,
	})
	return err
}

type balanceAssertion struct {
	Date   time.Time
	Amount model.Amount
}

// balanceAssertions folds the log into each account's asserted balances, in date order.
func balanceAssertions(log *eventlog.Log) (map[string][]balanceAssertion, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string][]balanceAssertion{}
	for _, e := range events {
		if e.Collection != CollectionBalance {
			continue
		}
		// A retire ends what came before it. Later assertions accumulate as usual, so an account that is
		// measured again after being retired simply anchors afresh.
		if e.Action == ActionRetired {
			delete(out, e.RecordID)
			continue
		}
		if e.Action != ActionAsserted {
			continue
		}
		var d balanceData
		if err := e.Decode(&d); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out[e.RecordID] = append(out[e.RecordID], balanceAssertion{Date: d.Date, Amount: d.Amount})
	}
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Date.Before(list[j].Date) })
	}
	return out, nil
}

// Reconciliation is one account's standing against the bank: what the bank last said it held, what the
// books fold to as of that date, and the difference. Reconciled is true when they agree to the penny.
//
// Stale says the verdict is old news: the books carry activity dated after this account's last bank
// figure, so "reconciled" speaks about AsOf and nothing since. An account whose connector quietly stops
// reporting a balance -- reading the wrong page, or never reading one at all -- goes on reconciling
// against the last figure recorded, in the same words an account measured this morning uses. Only the
// AS OF column tells them apart, and it is the easiest column to read past.
//
// Since and Anchored say how much of the account the verdict covers. The first assertion derives the
// opening balance -- the bank's figure less what the books fold to that day -- so the account agrees on
// that date by construction, and every line dated on or before it sits inside the derived offset where
// no error can produce a delta. Since is that date: the check is real from there forward and derived
// before it. Anchored means the account has only ever been measured once, so nothing has tested the
// derivation at all; it would agree whatever the account held.
type Reconciliation struct {
	Account    string
	AsOf       time.Time
	Since      time.Time
	Bank       model.Amount
	Books      model.Amount
	Delta      model.Amount
	Reconciled bool
	Stale      bool
	Anchored   bool
}

// staleAfterDays is how far an account's bank figure may sit behind the rest of the book before the
// gap stops being ordinary. The connectors of one cycle do not land together -- one bank is read
// tonight and another tomorrow morning, and a balance-only account trails either -- so a day or two of
// drift is the normal shape of a healthy book and must not be reported. A week is a different thing:
// by then the account has missed a cycle, and the reason is always that nothing is measuring it.
const staleAfterDays = 3

// Reconcile checks every account that carries a bank-stated balance: does the books balance, folded to
// the date of the latest assertion, agree with what the bank said there?
//
// The first assertion for an account is the anchor. Imports rarely reach back to the day an account was
// opened, so the books are short by that opening balance; the anchor derives it (the first bank figure
// less what the books fold to on its date) so the account matches by definition at that point. Every
// assertion after it is a real check: with the opening balance fixed, the books at the later date
// should equal the later bank figure exactly, and any difference is precisely a movement that was
// missed, duplicated, or wrongly paired since. Reconciliation is a fold; it records nothing.
func Reconcile(log *eventlog.Log) ([]Reconciliation, error) {
	txs, entries, err := Ledger(log)
	if err != nil {
		return nil, err
	}
	asserts, err := balanceAssertions(log)
	if err != nil {
		return nil, err
	}

	accounts := make([]string, 0, len(asserts))
	for a := range asserts {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)

	// How current the book is: the latest of anything it knows, whether a movement or a measurement.
	// The newest transaction alone is not enough -- an import that lands no new lines still records what
	// the bank said, and that is precisely the cycle an account left behind is being compared against.
	// An account whose own last bank figure predates this has been passed by: whatever its verdict, the
	// verdict is about its own AsOf.
	var booksThrough time.Time
	for _, tx := range txs {
		if tx.Date.After(booksThrough) {
			booksThrough = tx.Date
		}
	}
	for _, list := range asserts {
		if len(list) > 0 && list[len(list)-1].Date.After(booksThrough) {
			booksThrough = list[len(list)-1].Date
		}
	}

	var out []Reconciliation
	for _, account := range accounts {
		list := asserts[account]
		if len(list) == 0 {
			continue
		}
		first, latest := list[0], list[len(list)-1]
		commodity := latest.Amount.Commodity

		// offset is the opening balance the imports do not reach: the first bank figure, less what the
		// books fold to on that date.
		firstBooks := accountBalanceAsOf(txs, entries, account, first.Date, commodity)
		offset, err := first.Amount.Add(firstBooks.Negate())
		if err != nil {
			continue // the anchor is in another commodity; nothing to reconcile against
		}
		latestBooks := accountBalanceAsOf(txs, entries, account, latest.Date, commodity)
		books, err := offset.Add(latestBooks)
		if err != nil {
			continue
		}
		delta, err := latest.Amount.Add(books.Negate())
		if err != nil {
			continue
		}
		out = append(out, Reconciliation{
			Account: account, AsOf: latest.Date, Since: first.Date,
			Anchored: len(list) == 1,
			Bank:     latest.Amount, Books: books, Delta: delta,
			Reconciled: delta.IsZero(),
			Stale:      latest.Date.Before(booksThrough.AddDate(0, 0, -staleAfterDays)),
		})
	}
	return out, nil
}

// Balances folds the kept ledger into the current balance of every account you own, per commodity --
// what each of your accounts holds right now. A transfer booked from the far side lands in the counter
// account as a posting, so it is counted here too. This is the figure the account list shows.
func Balances(log *eventlog.Log) (map[string]map[string]model.Amount, error) {
	txs, entries, err := Ledger(log)
	if err != nil {
		return nil, err
	}
	owned, err := OwnedAccounts(log)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]model.Amount{}
	for a := range owned {
		out[a] = map[string]model.Amount{}
	}
	add := func(account string, amt model.Amount) {
		per, ok := out[account]
		if !ok {
			return // only accounts you own
		}
		cur, ok := per[amt.Commodity]
		if !ok {
			cur = model.Amount{Commodity: amt.Commodity}
		}
		if next, err := cur.Add(amt); err == nil {
			per[amt.Commodity] = next
		}
	}
	for i, tx := range txs {
		add(entries[i].SourceAccount(tx), tx.Amount)
		for _, p := range entries[i].Postings {
			add(p.Account, p.Amount)
		}
	}
	return out, nil
}

// accountBalanceAsOf folds the kept ledger into one account's balance on a date, in one commodity: the
// source amounts of its own lines plus the postings other entries make into it (a transfer booked from
// the far side lands here as a posting), counting only what is dated on or before asOf. It rolls the
// subtree up -- an account carries its descendants too -- so a parent a connector registered on
// reconciles to the sum of the purpose children its charges route to, which is what lets one physical
// card be split by purpose and still tie to its one bank balance. This mirrors the balance sheet,
// narrowed to one account and everything filed under it.
func accountBalanceAsOf(txs []model.Transaction, entries []model.Entry, account string, asOf time.Time, commodity string) model.Amount {
	sum := model.Amount{Commodity: commodity}
	add := func(a model.Amount) {
		if a.Commodity != commodity {
			return
		}
		if next, err := sum.Add(a); err == nil {
			sum = next
		}
	}
	within := func(a string) bool { return a == account || strings.HasPrefix(a, account+":") }
	for i, tx := range txs {
		if tx.Date.After(asOf) {
			continue
		}
		if within(entries[i].SourceAccount(tx)) {
			add(tx.Amount)
		}
		for _, p := range entries[i].Postings {
			if within(p.Account) {
				add(p.Amount)
			}
		}
	}
	return sum
}
