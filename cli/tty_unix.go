//go:build linux || darwin

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is a real terminal -- a keyboard -- as opposed to a pipe, a file, or a
// character device like /dev/null (all of which a looser check mistakes for a terminal). It asks the
// kernel for the terminal attributes; only a terminal answers. This is the standard isatty, kept to
// the standard library so the binary stays dependency free.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), ioctlReadTermios,
		uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return errno == 0
}
