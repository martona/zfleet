//go:build !linux

package collect

import "syscall"

// Non-Linux builds exist for dev and replay only; no parent-death signal.
func streamProcAttr() *syscall.SysProcAttr { return nil }
