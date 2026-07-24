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

// DueAccounts folds every Liabilities: account with a nonzero balance into its recorded due date and
// minimum payment, sorted soonest-due first (an account with no due date sorts last), so a routine
// bookwork pass can ask "which accounts need a payment soon and how much" instead of eyeballing
// nonzero balances and cross-checking the bank site by hand. An account that owes money but has never
// had -meta due/minimum set still appears, with blank columns, as a nudge to fill them in.
func DueAccounts(log *eventlog.Log) ([]DueAccount, error) {
	balances, err := Balances(log)
	if err != nil {
		return nil, err
	}
	meta, err := AccountMeta(log)
	if err != nil {
		return nil, err
	}

	accounts := make([]string, 0, len(balances))
	for a, per := range balances {
		if !isLiability(a) || !anyNonZero(per) {
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
			Balance: balances[a],
			Due:     meta[a]["due"],
			Minimum: meta[a]["minimum"],
		})
	}
	return out, nil
}

func isLiability(account string) bool {
	return account == "Liabilities" || strings.HasPrefix(account, "Liabilities:")
}

func anyNonZero(per map[string]model.Amount) bool {
	for _, amt := range per {
		if !amt.IsZero() {
			return true
		}
	}
	return false
}
