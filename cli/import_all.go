package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/store"
)

// orderConnectorsByLogin clusters connectors that share a login (their token-env) so `import -all` can
// run each group back to back and sign in once for the lot. Groups and their members keep their
// first-appearance order, so this is a stable reordering, never a sort.
func orderConnectorsByLogin(cs []books.Connector) []books.Connector {
	order := make([]string, 0, len(cs))
	groups := map[string][]books.Connector{}
	for _, c := range cs {
		if _, ok := groups[c.TokenEnv]; !ok {
			order = append(order, c.TokenEnv)
		}
		groups[c.TokenEnv] = append(groups[c.TokenEnv], c)
	}
	out := make([]books.Connector, 0, len(cs))
	for _, te := range order {
		out = append(out, groups[te]...)
	}
	return out
}

// importableConnectors splits the registered connectors into those with an importer (the banks) and
// those without (rentapp is export-only), so `import -all` runs the first and names the second rather
// than erroring on a kind it cannot read.
func importableConnectors(cs []books.Connector) (importable, skipped []books.Connector) {
	for _, c := range cs {
		if source.SupportsBank(c.Kind) {
			importable = append(importable, c)
		} else {
			skipped = append(skipped, c)
		}
	}
	return importable, skipped
}

// importEachConnector runs each connector in turn, keeping going when one fails so a single expired
// sign-in or selector drift does not abort the batch; it returns the names that failed. A login is
// refreshed at most once: the -relogin carried on base applies to the first connector of each token-env
// and is cleared for the rest, which reuse the session it just saved -- the point of ordering shared
// logins together.
func importEachConnector(ordered []books.Connector, base fetchOpts, run func(books.Connector, fetchOpts) error) []string {
	seen := map[string]bool{}
	var failed []string
	for _, c := range ordered {
		o := base
		if seen[c.TokenEnv] {
			o.relogin = false
		}
		seen[c.TokenEnv] = true
		if err := run(c, o); err != nil {
			failed = append(failed, c.Name)
		}
	}
	return failed
}

// importAllConnectors imports every registered connector that can be imported. It orders connectors
// that share a login together so each sign-in serves its whole group, refreshes a login at most once
// under -relogin, and continues past a connector that fails so one expired session does not stop the
// rest. args are the flags after -all (which the caller has already stripped).
func importAllConnectors(args []string) error {
	fs := flag.NewFlagSet("import -all", flag.ExitOnError)
	relogin := fs.Bool("relogin", false, "sign in fresh once per login, ignoring any saved session")
	history := fs.Int("history", 0, "days of history to read this run (relative; overrides each connector default)")
	fromFlag := fs.String("from", "", "backfill start date, e.g. 2026-02-01 or \"Feb 1, 2026\" (overrides -history)")
	toFlag := fs.String("to", "", "backfill end date; defaults to today when -from is given")
	if err := fs.Parse(args); err != nil {
		return err
	}
	from, to, err := backfillRange(*fromFlag, *toFlag)
	if err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	cs, err := books.Connectors(s.Log)
	if err != nil {
		return err
	}
	importable, skipped := importableConnectors(cs)
	for _, c := range skipped {
		fmt.Printf("skipping %s: %s is export-only\n", c.Name, c.Kind)
	}
	ordered := orderConnectorsByLogin(importable)
	if len(ordered) == 0 {
		fmt.Println("no importable connectors are registered")
		return nil
	}

	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	base := fetchOpts{
		sessionDir:  dir,
		snapshotDir: filepath.Join(s.Path, "snapshots"),
		interactive: interactiveTerminal(),
		relogin:     *relogin,
		history:     *history,
		from:        from,
		to:          to,
	}

	failed := importEachConnector(ordered, base, func(c books.Connector, o fetchOpts) error {
		fmt.Printf("\n== %s ==\n", c.Name)
		if err := importConnector(s.Log, c, o); err != nil {
			fmt.Printf("%s: %v\n", c.Name, err)
			return err
		}
		return nil
	})
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d connectors failed: %s", len(failed), len(ordered), strings.Join(failed, ", "))
	}
	return nil
}
