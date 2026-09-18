//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package term

import (
	"errors"
	"os"
)

// Platforms without a terminal implementation still have to compile; the UI
// reports the error and the caller falls back to headless mode.

// State is a placeholder on unsupported platforms.
type State struct{}

// IsTerminal always reports false here.
func IsTerminal(*os.File) bool { return false }

// MakeRaw is unsupported.
func MakeRaw(*os.File) (*State, error) {
	return nil, errors.New("raw terminal mode is not supported on this platform")
}

// Restore is a no-op.
func (*State) Restore(*os.File) error { return nil }

// Size is unsupported.
func Size(*os.File) (int, int, error) {
	return 0, 0, errors.New("terminal size is not supported on this platform")
}

// EnableVT is a no-op.
func EnableVT(*os.File) error { return nil }
