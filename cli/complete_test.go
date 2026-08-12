package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func noConnectors() []string { return nil }

func has(cands []string, want string) bool {
	for _, c := range cands {
		if c == want {
			return true
		}
	}
	return false
}

func TestCompleteTopLevelCommands(t *testing.T) {
	got, dir := completeArgs([]string{""}, noConnectors)
	for _, want := range []string{"init", "import", "connectors", "rules", "books", "report", "export", "completion", "help"} {
		if !has(got, want) {
			t.Errorf("top-level completion missing %q; got %v", want, got)
		}
	}
	if has(got, "__complete") {
		t.Errorf("the internal __complete command must stay out of completion; got %v", got)
	}
	if dir != compNoFiles {
		t.Errorf("a command name is not a file, so no file completion; got directive %d", dir)
	}
}

func TestCompleteSubcommands(t *testing.T) {
	got, dir := completeArgs([]string{"connectors", ""}, noConnectors)
	sort.Strings(got)
	want := []string{"list", "register", "rm"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connectors subcommands = %v, want %v", got, want)
	}
	if dir != compNoFiles {
		t.Errorf("a subcommand is not a file; got directive %d", dir)
	}
}

func TestCompleteRulesSubcommands(t *testing.T) {
	got, _ := completeArgs([]string{"rules", ""}, noConnectors)
	sort.Strings(got)
	want := []string{"list", "mv", "rm", "set"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules subcommands = %v, want %v", got, want)
	}
}

func TestCompleteConnectorNamesForImport(t *testing.T) {
	conns := func() []string { return []string{"rent", "acme-chq"} }
	got, dir := completeArgs([]string{"import", ""}, conns)
	if !has(got, "rent") || !has(got, "acme-chq") {
		t.Errorf("import should complete connector names; got %v", got)
	}
	if dir != compDefault {
		t.Errorf("import's first argument may be a file, so files stay allowed; got directive %d", dir)
	}
}

func TestCompleteConnectorNamesForExport(t *testing.T) {
	conns := func() []string { return []string{"rent"} }
	got, dir := completeArgs([]string{"export", ""}, conns)
	if !has(got, "rent") {
		t.Errorf("export should complete connector names; got %v", got)
	}
	if dir != compNoFiles {
		t.Errorf("export takes only a connector, never a file; got directive %d", dir)
	}
}

func TestCompleteConnectorNamesForConnectorsRm(t *testing.T) {
	conns := func() []string { return []string{"rent"} }
	got, _ := completeArgs([]string{"connectors", "rm", ""}, conns)
	if !has(got, "rent") {
		t.Errorf("connectors rm should complete existing connector names; got %v", got)
	}
}

func TestConnectorRegisterTakesNoNameCompletion(t *testing.T) {
	called := false
	conns := func() []string { called = true; return []string{"rent"} }
	got, _ := completeArgs([]string{"connectors", "register", ""}, conns)
	if len(got) != 0 {
		t.Errorf("register names a NEW connector; there is nothing to complete, got %v", got)
	}
	if called {
		t.Errorf("register must not query the existing connectors")
	}
}

func TestCompleteRulesSetFlags(t *testing.T) {
	got, dir := completeArgs([]string{"rules", "set", "acme", "-"}, noConnectors)
	for _, want := range []string{"-category", "-payee", "-tax-rate", "-why", "-actor"} {
		if !has(got, want) {
			t.Errorf("rules set flag completion missing %q; got %v", want, got)
		}
	}
	if dir != compNoFiles {
		t.Errorf("a flag is not a file; got directive %d", dir)
	}
}

func TestCompleteImportFlags(t *testing.T) {
	got, _ := completeArgs([]string{"import", "-"}, noConnectors)
	for _, want := range []string{"-history", "-from", "-to", "-account", "-format"} {
		if !has(got, want) {
			t.Errorf("import flag completion missing %q; got %v", want, got)
		}
	}
}

func TestConnectorRegisterHistoryFlagCompletes(t *testing.T) {
	// -history is a real flag on connectors register; the synopsis and completion must both carry it.
	got, _ := completeArgs([]string{"connectors", "register", "rent", "-"}, noConnectors)
	if !has(got, "-history") {
		t.Errorf("connectors register should complete -history; got %v", got)
	}
}

func TestCompleteHelpTopics(t *testing.T) {
	got, _ := completeArgs([]string{"help", ""}, noConnectors)
	if !has(got, "import") || !has(got, "rules") {
		t.Errorf("help should complete command names; got %v", got)
	}
}

func TestCompleteShellNames(t *testing.T) {
	got, _ := completeArgs([]string{"completion", ""}, noConnectors)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"bash", "fish", "zsh"}) {
		t.Errorf("completion should complete the shells it can emit; got %v", got)
	}
}

func TestRunCompleteEmitsCandidatesThenDirective(t *testing.T) {
	var b strings.Builder
	runComplete(&b, []string{""}, noConnectors)
	out := b.String()
	if !strings.Contains(out, "import\n") {
		t.Errorf("expected command names in the output; got %q", out)
	}
	if !strings.HasSuffix(out, ":4\n") {
		t.Errorf("expected the trailing :4 (no-file) directive; got %q", out)
	}
}

func TestCompletionScriptCallsBackIntoComplete(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var b strings.Builder
		if err := completionScript(&b, shell); err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		if !strings.Contains(b.String(), "__complete") {
			t.Errorf("%s completion script should call `bkpr __complete`", shell)
		}
	}
}

func TestCompletionScriptRejectsUnknownShell(t *testing.T) {
	var b strings.Builder
	if err := completionScript(&b, "powershell"); err == nil {
		t.Errorf("an unknown shell should be an error, not an empty script")
	}
}

func TestUsageHidesInternalCompleteCommand(t *testing.T) {
	var b strings.Builder
	writeUsage(&b, palette{})
	if strings.Contains(b.String(), "__complete") {
		t.Errorf("the usage screen must not show the internal __complete command")
	}
}
