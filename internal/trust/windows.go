//go:build windows

package trust

import "strings"

func platformAvailable() bool { return hasCommand("certutil") }

func platformCheck(m *Manager) (Status, error) {
	out, err := run("certutil", "-user", "-store", "ROOT")
	if err != nil {
		return Status{}, err
	}
	if strings.Contains(out, m.CommonName) {
		return Status{Installed: true, Detail: "Windows user root store"}, nil
	}
	return Status{Installed: false, Detail: "not in the Windows user root store"}, nil
}

func platformInstall(m *Manager, system bool) (string, error) {
	if system {
		// A machine-wide install raises a UAC dialog; no terminal is needed.
		if _, err := run("certutil", "-addstore", "ROOT", m.CertPath); err != nil {
			return "", err
		}
		return "Windows machine root store (all users)", nil
	}
	if _, err := run("certutil", "-user", "-addstore", "ROOT", m.CertPath); err != nil {
		return "", err
	}
	return "Windows user root store", nil
}

func platformUninstall(m *Manager, system bool) error {
	args := []string{"-delstore", "ROOT", m.CommonName}
	if !system {
		args = append([]string{"-user"}, args...)
	}
	if _, err := run("certutil", args...); err != nil {
		if strings.Contains(err.Error(), "not find") || strings.Contains(err.Error(), "Cannot find") {
			return nil
		}
		return err
	}
	return nil
}
