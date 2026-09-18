//go:build windows

package term

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
	procGetConsoleInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

const (
	enableVirtualTerminalProcessing = 0x0004
	enableProcessedOutput           = 0x0001
	enableEchoInput                 = 0x0004
	enableLineInput                 = 0x0002
	enableProcessedInput            = 0x0001
)

// State captures the previous console mode.
type State struct {
	inMode  uint32
	outMode uint32
	in      syscall.Handle
	out     syscall.Handle
	valid   bool
}

// IsTerminal reports whether f is a console handle.
func IsTerminal(f *os.File) bool {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

// MakeRaw switches the console into a raw, VT-enabled mode.
func MakeRaw(f *os.File) (*State, error) {
	out := syscall.Handle(os.Stdout.Fd())
	in := syscall.Handle(f.Fd())
	st := &State{in: in, out: out}

	if r, _, err := procGetConsoleMode.Call(uintptr(in), uintptr(unsafe.Pointer(&st.inMode))); r == 0 {
		return nil, err
	}
	if r, _, err := procGetConsoleMode.Call(uintptr(out), uintptr(unsafe.Pointer(&st.outMode))); r == 0 {
		return nil, err
	}
	inMode := st.inMode &^ (enableEchoInput | enableLineInput | enableProcessedInput)
	if r, _, err := procSetConsoleMode.Call(uintptr(in), uintptr(inMode)); r == 0 {
		return nil, err
	}
	outMode := st.outMode | enableProcessedOutput | enableVirtualTerminalProcessing
	if r, _, err := procSetConsoleMode.Call(uintptr(out), uintptr(outMode)); r == 0 {
		return nil, err
	}
	st.valid = true
	return st, nil
}

// Restore undoes MakeRaw.
func (s *State) Restore(f *os.File) error {
	if s == nil || !s.valid {
		return nil
	}
	procSetConsoleMode.Call(uintptr(s.in), uintptr(s.inMode))
	procSetConsoleMode.Call(uintptr(s.out), uintptr(s.outMode))
	return nil
}

type coord struct{ X, Y int16 }
type smallRect struct{ Left, Top, Right, Bottom int16 }
type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

// Size returns the console window size in cells.
func Size(f *os.File) (int, int, error) {
	out := syscall.Handle(os.Stdout.Fd())
	var info consoleScreenBufferInfo
	r, _, err := procGetConsoleInfo.Call(uintptr(out), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, 0, err
	}
	w := int(info.Window.Right-info.Window.Left) + 1
	h := int(info.Window.Bottom-info.Window.Top) + 1
	if w <= 0 || h <= 0 {
		return 0, 0, errors.New("unknown console size")
	}
	return w, h, nil
}

// EnableVT turns on virtual terminal processing for stdout.
func EnableVT(f *os.File) error {
	out := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, err := procGetConsoleMode.Call(uintptr(out), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return err
	}
	mode |= enableProcessedOutput | enableVirtualTerminalProcessing
	if r, _, err := procSetConsoleMode.Call(uintptr(out), uintptr(mode)); r == 0 {
		return err
	}
	return nil
}
