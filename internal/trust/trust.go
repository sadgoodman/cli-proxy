// Package trust installs the cli-proxy root certificate into the operating
// system's trust store and removes it again.
//
// A one-keystroke install is only useful if it is also a one-keystroke
// uninstall, so every operation here is paired and reportable.
package trust

import (
	"errors"
	"strings"
)

// ErrUnsupported is returned when the platform has no implementation.
var ErrUnsupported = errors.New("certificate installation is not implemented on this platform")

// Status describes whether the root is currently trusted.
type Status struct {
	Installed bool
	Detail    string // where it lives, or why it is not installed
}

// String renders the status for the UI.
func (s Status) String() string {
	if s.Installed {
		if s.Detail == "" {
			return "trusted"
		}
		return "trusted — " + s.Detail
	}
	if s.Detail == "" {
		return "not installed"
	}
	return s.Detail
}

// Manager performs trust store operations for one certificate.
type Manager struct {
	CertPath   string
	CommonName string
}

// New creates a manager for the certificate at certPath.
func New(certPath, commonName string) *Manager {
	return &Manager{CertPath: certPath, CommonName: commonName}
}

// Indirect hooks keep the platform commands testable.
var (
	availableFn = platformAvailable
	checkFn     = platformCheck
	installFn   = platformInstall
	uninstallFn = platformUninstall
)

// Available reports whether this platform can install certificates.
func (m *Manager) Available() bool { return availableFn() }

// Check reports the current trust state.
func (m *Manager) Check() (Status, error) { return checkFn(m) }

// Install adds the certificate to the trust store. When system is true the
// machine-wide store is targeted, which usually requires administrator rights.
func (m *Manager) Install(system bool) (string, error) { return installFn(m, system) }

// Uninstall removes the certificate again.
func (m *Manager) Uninstall(system bool) error { return uninstallFn(m, system) }

// Target describes what will be modified, for confirmation messages.
func Target(system bool) string {
	if system {
		return "the system-wide trust store (needs administrator rights)"
	}
	return "the trust store of this user"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
