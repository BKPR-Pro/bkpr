package source

import (
	"strings"
	"testing"
)

// scanProgress separates the script's progress markers (for a live status display) from its real
// error output (for reporting a failure), so one stream carries both without the markers polluting
// an error message.
func TestScanProgressSplitsMarkersFromErrors(t *testing.T) {
	in := progressPrefix + "signing in\n" +
		"a real error line\n" +
		progressPrefix + "reading transactions\n" +
		"another error\n"

	var stages []string
	errText := scanProgress(strings.NewReader(in), func(s string) { stages = append(stages, s) })

	if strings.Join(stages, "|") != "signing in|reading transactions" {
		t.Errorf("stages = %v, want the two progress lines", stages)
	}
	if strings.Contains(errText, "signing in") || strings.Contains(errText, "reading transactions") {
		t.Errorf("progress leaked into the error text: %q", errText)
	}
	if !strings.Contains(errText, "a real error line") || !strings.Contains(errText, "another error") {
		t.Errorf("error text lost a real line: %q", errText)
	}
}

// A nil callback is fine: progress is simply dropped, and the error text still comes through.
func TestScanProgressToleratesNilCallback(t *testing.T) {
	errText := scanProgress(strings.NewReader(progressPrefix+"working\noops\n"), nil)
	if strings.TrimSpace(errText) != "oops" {
		t.Errorf("errText = %q, want just the error line", errText)
	}
}
