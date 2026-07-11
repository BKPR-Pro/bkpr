package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/dallasread/bookkeeper/lib/adapters/rentapp"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// rentappLeaseKey is the metadata key the rentapp connector reads off a categorized line. A
// connector owns the metadata namespace equal to its kind, so the lease a rent deposit belongs to
// travels as rentapp.lease, set on the tenant's rule where the description already identifies them.
const rentappLeaseKey = "rentapp.lease"

// exportPlan is one rent deposit ready to record: which lease, how much, when.
type exportPlan struct {
	txID   string
	lease  string
	amount int64 // cents
	date   string
	payee  string
}

// exportResult reports what an export did, or would do on a dry run.
type exportResult struct {
	Planned  []exportPlan // what a dry run would send
	Recorded []exportPlan // what was recorded this run
	Failed   []exportFailure
}

type exportFailure struct {
	plan exportPlan
	err  error
}

// exportPlans folds the books and selects the rent deposits that carry a lease and have not been
// exported yet. It has no side effect, so a dry run and the real export agree on what to send. A
// deposit no rule attributed to a lease is left alone: there is nothing to record it against, and
// it is never guessed.
func exportPlans(log *eventlog.Log) ([]exportPlan, error) {
	txs, entries, err := books.Ledger(log)
	if err != nil {
		return nil, err
	}
	exported, err := books.Exported(log)
	if err != nil {
		return nil, err
	}

	var plans []exportPlan
	for i, tx := range txs {
		lease := entries[i].Metadata[rentappLeaseKey]
		if lease == "" || exported[tx.ID] {
			continue
		}
		cents, err := amountCents(tx.Amount)
		if err != nil {
			return nil, fmt.Errorf("export: %s: %w", tx.ID, err)
		}
		plans = append(plans, exportPlan{
			txID: tx.ID, lease: lease, amount: cents,
			date: tx.Date.Format("2006-01-02"), payee: entries[i].Payee,
		})
	}
	return plans, nil
}

// exportRent records each planned rent deposit against its lease in the rent app, then marks it
// exported. The deposit's fingerprint is the idempotency key, so a repeat is safe on both sides:
// the rent app records at most once per key, and an exported deposit is skipped here next time.
// Without confirm it is a dry run: the plan is returned and nothing is sent. A lease the rent app
// rejects is reported rather than swallowed, and is not marked exported, so a fixed lease id retries.
func exportRent(log *eventlog.Log, client *rentapp.Client, connector string, confirm bool) (exportResult, error) {
	plans, err := exportPlans(log)
	if err != nil {
		return exportResult{}, err
	}
	if !confirm {
		return exportResult{Planned: plans}, nil
	}

	var res exportResult
	for _, p := range plans {
		rec, err := client.RecordRent(rentapp.Payment{
			LeaseID: p.lease, AmountCents: p.amount, PaidOn: p.date, IdempotencyKey: p.txID,
		})
		if err != nil {
			res.Failed = append(res.Failed, exportFailure{plan: p, err: err})
			continue
		}
		if err := books.RecordExport(log, "export:"+connector, p.txID, connector, p.lease, rec.ID, p.amount); err != nil {
			return res, err
		}
		res.Recorded = append(res.Recorded, p)
	}
	if len(res.Failed) > 0 {
		return res, fmt.Errorf("export: %d of %d deposits failed to record", len(res.Failed), len(plans))
	}
	return res, nil
}

// amountCents renders an amount as integer cents, the unit the rent app records. Sub-cent precision
// cannot be a rent payment, so it is refused rather than rounded.
func amountCents(a model.Amount) (int64, error) {
	switch {
	case a.Scale == 2:
		return a.Units, nil
	case a.Scale < 2:
		m := int64(1)
		for i := a.Scale; i < 2; i++ {
			m *= 10
		}
		return a.Units * m, nil
	default:
		m := int64(1)
		for i := uint8(2); i < a.Scale; i++ {
			m *= 10
		}
		if a.Units%m != 0 {
			return 0, fmt.Errorf("amount %s has sub-cent precision", a)
		}
		return a.Units / m, nil
	}
}

// exportCmd writes rent the books already booked out to a connector, so its paid/unpaid state stays
// current. It is a dry run unless -confirm is given, since it writes to a live system.
func exportCmd(args []string) error {
	name, rest, err := firstArg(args, "the connector to export to, e.g. rent")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "record the payments; without it, a dry run")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	dest, ok, err := books.ConnectorByName(s.Log, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("export: no connector named %q; register it with `connectors add`", name)
	}
	if dest.Kind != "rentapp" {
		return fmt.Errorf("export: connector %q has kind %q, which cannot be exported to", name, dest.Kind)
	}
	token := os.Getenv(dest.TokenEnv)
	if token == "" {
		return fmt.Errorf("export: %s is empty; set it to the rent app's token", dest.TokenEnv)
	}

	res, err := exportRent(s.Log, rentapp.New(dest.URL, token), name, *confirm)
	reportExport(os.Stdout, res, *confirm)
	return err
}

// reportExport prints what the export did or would do, so a dry run reads the same as the real thing
// with the verb changed.
func reportExport(out io.Writer, res exportResult, confirm bool) {
	if !confirm {
		if len(res.Planned) == 0 {
			fmt.Fprintln(out, "nothing to export: no unrecorded rent deposits carry a lease")
			return
		}
		fmt.Fprintf(out, "would record %d rent payment(s) (dry run; add -confirm to send):\n", len(res.Planned))
		writePlans(out, res.Planned)
		return
	}

	fmt.Fprintf(out, "recorded %d rent payment(s)\n", len(res.Recorded))
	writePlans(out, res.Recorded)
	for _, f := range res.Failed {
		fmt.Fprintf(out, "FAILED %s to lease %s: %v\n", f.plan.date, f.plan.lease, f.err)
	}
}

func writePlans(out io.Writer, plans []exportPlan) {
	if len(plans) == 0 {
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tPAYEE\tLEASE\tAMOUNT")
	for _, p := range plans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d.%02d\n", p.date, p.payee, p.lease, p.amount/100, p.amount%100)
	}
	w.Flush()
}
