package main

import (
	"flag"
	"fmt"
	"time"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/store"
)

// balanceCmd records what an account holds, by hand, for the accounts no connector anchors -- a
// statement imported from CSV or ledger. Its one subcommand today is `set`; keeping the verb around
// it leaves room for `balance` to grow (a list, a clear) without the grammar moving.
func balanceCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("balance needs a subcommand: set, rm")
	}
	switch args[0] {
	case "set":
		return balanceSet(args[1:])
	case "rm":
		return balanceRm(args[1:])
	default:
		return fmt.Errorf("unknown balance subcommand %q; try set or rm", args[0])
	}
}

// balanceRm retires an account's reconciliation: the account is finished and reconcile should stop
// reporting it. Nothing is deleted -- its transactions and its recorded balances stay in the log, and
// recording a balance afterwards starts it over. This is the verb for an account that was emptied
// rather than one that was wrong: a connector re-pointed away from it, or its lines collapsed onto the
// account they belonged to. Asserting zero is not the same thing, because the first assertion for an
// account derives its opening balance -- the account then matches by construction however little it
// holds, and a later zero reads as a delta the size of that derived offset rather than as a close.
func balanceRm(args []string) error {
	account, rest, err := firstArg(args, "the account to stop reconciling")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("balance rm", flag.ExitOnError)
	actor := fs.String("actor", "human", "who retired the account; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	if err := books.RetireBalance(s.Log, *actor, account); err != nil {
		return err
	}
	fmt.Printf("retired: %s is no longer reconciled; its history is untouched\n", account)
	return nil
}

// balanceSet anchors a file-imported account the way a connector anchors a live one: it records the
// balance the account held on a date, the ground truth reconcile checks the books against. A
// liability is entered as the statement shows it -- a positive amount owing -- and stored negative,
// the same sign treatment a scraped balance gets, so a hand-set anchor and a scraped one agree.
func balanceSet(args []string) error {
	account, rest, err := firstArg(args, "the account to record a balance for")
	if err != nil {
		return err
	}
	amountArg, rest, err := firstArg(rest, "the balance the account holds, e.g. \"100.00 CAD\"")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("balance set", flag.ExitOnError)
	asOf := fs.String("as-of", "", "the date the balance was true (YYYY-MM-DD); today if omitted")
	actor := fs.String("actor", "human", "who recorded the balance; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	amount, err := model.ParseAmount(amountArg)
	if err != nil {
		return err
	}
	date := time.Now()
	if *asOf != "" {
		if date, err = parseAsOf(*asOf); err != nil {
			return err
		}
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	bal := reconcileBalance(account, amount)
	if err := books.AssertBalance(s.Log, *actor, account, date, bal); err != nil {
		return err
	}
	fmt.Printf("balance recorded: %s reconciles %s as of %s\n", bal, account, date.Format("2006-01-02"))
	return nil
}
