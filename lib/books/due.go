package books

import (
	"sort"
	"strings"

	"github.com/dallasread/bkpr/lib/eventlog"
	"github.com/dallasread/bkpr/lib/model"
)

// DueAccount is one liability account you owe against right now: its balance, and the due date /
// minimum payment recorded on it via `accounts set -meta due=... minimum=...`, if any.
type DueAccount struct {
	Account string
	Balance map[string]model.Amount
	Due     string
	Minimum string
}

// DueAccounts folds every Liabilities: account family into its recorded due date and minimum payment,
// sorted soonest-due first (a family with no due date sorts last), so a routine bookwork pass can ask
// "which accounts need a payment soon and how much" instead of eyeballing nonzero balances and
// cross-checking the bank site by hand. A family is the bare physical account (Liabilities:<card>) plus
// every purpose-split :child account filed under it -- a split can leave one bucket looking positive
// even though the card overall is owed money, so the balances are netted before anything is reported.
// The due date and minimum are read from the bare parent, since that is where -meta is set. A family
// that owes money but has never had -meta due/minimum set still appears, with blank columns, as a nudge
// to fill them in.
func DueAccounts(log *eventlog.Log) ([]DueAccount, error) {
	balances, err := Balances(log)
	if err != nil {
		return nil, err
	}
	meta, err := AccountMeta(log)
	if err != nil {
		return nil, err
	}

	totals := map[string]map[string]model.Amount{}
	for a, per := range balances {
		if !isLiability(a) {
			continue
		}
		root := liabilityFamily(a)
		rt, ok := totals[root]
		if !ok {
			rt = map[string]model.Amount{}
			totals[root] = rt
		}
		for commodity, amt := range per {
			cur, ok := rt[commodity]
			if !ok {
				cur = model.Amount{Commodity: commodity}
			}
			if next, err := cur.Add(amt); err == nil {
				rt[commodity] = next
			}
		}
	}

	accounts := make([]string, 0, len(totals))
	for a, per := range totals {
		if !anyNonZero(per) {
			continue
		}
		accounts = append(accounts, a)
	}
	sort.Slice(accounts, func(i, j int) bool {
		di, dj := meta[accounts[i]]["due"], meta[accounts[j]]["due"]
		if di != dj {
			if di == "" {
				return false
			}
			if dj == "" {
				return true
			}
			return di < dj
		}
		return accounts[i] < accounts[j]
	})

	out := make([]DueAccount, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, DueAccount{
			Account: a,
			Balance: totals[a],
			Due:     meta[a]["due"],
			Minimum: meta[a]["minimum"],
		})
	}
	return out, nil
}

func isLiability(account string) bool {
	return account == "Liabilities" || strings.HasPrefix(account, "Liabilities:")
}

// liabilityFamily is the physical account a liability leaf belongs to: Liabilities: plus its first
// segment, so a purpose-split child (Liabilities:RBC Mastercard:Consulting) rolls up under the same
// row as its bare parent (Liabilities:RBC Mastercard), however many segments the split itself carries.
func liabilityFamily(account string) string {
	rest := strings.TrimPrefix(account, "Liabilities:")
	if rest == account {
		return account // "Liabilities" itself, with no child segment
	}
	if idx := strings.Index(rest, ":"); idx >= 0 {
		return "Liabilities:" + rest[:idx]
	}
	return account
}

func anyNonZero(per map[string]model.Amount) bool {
	for _, amt := range per {
		if !amt.IsZero() {
			return true
		}
	}
	return false
}
