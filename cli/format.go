package main

import "os"

// defaultFormat picks a command's -format default the same way the usage screen already picks its
// palette: human when a person is watching a real terminal, machine when stdout is a pipe, a
// redirect, or another process reading it -- a script, a cron job, an agent. A command that wants
// something other than these two (books' -format ledger, a genuine artifact rather than a reading)
// stays reachable only by naming it explicitly; it is never a default either way.
func defaultFormat(stdout *os.File, human, machine string) string {
	if isTerminal(stdout) {
		return human
	}
	return machine
}
