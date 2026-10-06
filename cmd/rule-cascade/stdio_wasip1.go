//go:build wasip1

package main

import "syscall"

// On WASI the Go runtime switches standard input, output and error to non-blocking mode and then
// asks the host whether that worked. Node.js (uvwasi) applies the change without reporting it, so
// a read fails with EAGAIN whenever no request is waiting and a write fails when a pipe is full.
// This program has nothing else to do while it waits: it uses blocking I/O on every host.
func init() {
	for fd := 0; fd <= 2; fd++ {
		_ = syscall.SetNonblock(fd, false)
	}
}
