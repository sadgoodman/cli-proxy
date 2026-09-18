//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package term

import "syscall"

// The BSD family shares the TIOCGETA/TIOCSETA ioctls.
const (
	ioctlReadTermios  = syscall.TIOCGETA
	ioctlWriteTermios = syscall.TIOCSETA
)
