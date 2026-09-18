//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

// Package term provides raw-mode terminal handling, window size queries and a
// dependency-free parser for keyboard and SGR mouse escape sequences.
package term

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// State captures the terminal attributes so they can be restored.
type State struct {
	termios syscall.Termios
	valid   bool
}

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	return errno
}

// IsTerminal reports whether f is attached to a terminal.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var t syscall.Termios
	return ioctl(f.Fd(), ioctlReadTermios, unsafe.Pointer(&t)) == 0
}

// MakeRaw switches the terminal into raw mode and returns the previous state.
func MakeRaw(f *os.File) (*State, error) {
	if f == nil {
		return nil, errors.New("nil file")
	}
	fd := f.Fd()
	var t syscall.Termios
	if err := ioctl(fd, ioctlReadTermios, unsafe.Pointer(&t)); err != 0 {
		return nil, err
	}
	old := t

	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0

	if err := ioctl(fd, ioctlWriteTermios, unsafe.Pointer(&t)); err != 0 {
		return nil, err
	}
	return &State{termios: old, valid: true}, nil
}

// Restore returns the terminal to the state captured by MakeRaw.
func (s *State) Restore(f *os.File) error {
	if s == nil || !s.valid || f == nil {
		return nil
	}
	t := s.termios
	return ioctl(f.Fd(), ioctlWriteTermios, unsafe.Pointer(&t))
}

// Size returns the terminal width and height in cells.
func Size(f *os.File) (int, int, error) {
	if f == nil {
		return 0, 0, errors.New("nil file")
	}
	var ws winsize
	if err := ioctl(f.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != 0 {
		return 0, 0, err
	}
	if ws.Col == 0 || ws.Row == 0 {
		return 0, 0, errors.New("unknown terminal size")
	}
	return int(ws.Col), int(ws.Row), nil
}

// EnableVT is a no-op outside Windows.
func EnableVT(f *os.File) error { return nil }
