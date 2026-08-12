//go:build !linux && !darwin

package main

import "os"

// isTerminal without a known terminal query assumes a person is present, so a sign-in is never
// wrongly refused on a platform this build does not specialize for.
func isTerminal(*os.File) bool { return true }
