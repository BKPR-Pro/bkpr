package source

import (
	"bufio"
	"io"
	"strings"
)

// A bank script reports its stage (starting a browser, signing in, reading transactions) so the CLI
// can show a live status while the browser works, which otherwise looks like a long hang. Progress
// and real error output share the script's stderr; a progress line is marked with progressPrefix so
// the two can be told apart -- the markers drive the status display, everything else is kept for
// reporting a failure. This must match PROGRESS_PREFIX in scripts/harness.js.
const progressPrefix = "@@BKPROG@@ "

// scanProgress reads the script's stderr line by line, handing each progress line to onProgress and
// collecting the rest as the error text. It returns once the stream is exhausted (the process closed
// its stderr), so callers read it to completion before waiting on the process.
func scanProgress(r io.Reader, onProgress func(string)) string {
	var errText strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if msg, ok := strings.CutPrefix(line, progressPrefix); ok {
			if onProgress != nil {
				onProgress(msg)
			}
			continue
		}
		errText.WriteString(line)
		errText.WriteByte('\n')
	}
	return errText.String()
}
