//go:build unix

package eventlog

import (
	"os"
	"syscall"
)

// lock takes an exclusive, non-blocking advisory lock on the file. The lock is released when the
// file is closed, so a single writer is guaranteed for the life of a run. This is what lets
// AppendOnce enforce uniqueness from an in-memory set: no second process can append behind its
// back. It is advisory, so it only holds against other bkpr processes, which is exactly the
// set that matters. syscall keeps this dependency-free.
func lock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
