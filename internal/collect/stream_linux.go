//go:build linux

package collect

import "syscall"

// streamProcAttr ties a stream child's life to ours: if zfleet dies by any
// means, the kernel delivers SIGTERM to the child. SIGTERM (not SIGKILL) so
// a local ssh client can close its channel and take the remote command down
// with it.
func streamProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
