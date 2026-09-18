//go:build linux

package trust

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// systemInstallCommand is the elevated command, also shown when no terminal
// emulator can be started automatically.
func systemInstallCommand(certPath string) string {
	return "sudo cp " + shellQuote(certPath) + " " + systemCertFile + " && sudo update-ca-certificates"
}

func systemUninstallCommand() string {
	return "sudo rm -f " + systemCertFile + " && sudo update-ca-certificates --fresh"
}

// openTerminal hands a command to a terminal emulator, keeping the password
// prompt out of our own input stream.
func openTerminal(command string) bool {
	type emu struct {
		name string
		args []string
	}
	candidates := []emu{
		{"x-terminal-emulator", []string{"-e", "sh", "-c", command}},
		{"gnome-terminal", []string{"--", "sh", "-c", command}},
		{"konsole", []string{"-e", "sh", "-c", command}},
		{"xfce4-terminal", []string{"-e", "sh -c " + shellQuote(command)}},
		{"xterm", []string{"-e", "sh", "-c", command}},
	}
	for _, c := range candidates {
		if !hasCommand(c.name) {
			continue
		}
		if err := runDetached(c.name, c.args...); err == nil {
			return true
		}
	}
	return false
}

const systemCertFile = "/usr/local/share/ca-certificates/cli-proxy.crt"

func platformAvailable() bool {
	return hasCommand("certutil") || hasCommand("update-ca-certificates")
}

// nssDB is the per-user NSS database used by Chrome and Chromium.
func nssDB() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pki", "nssdb")
}

func platformCheck(m *Manager) (Status, error) {
	if db := nssDB(); db != "" && hasCommand("certutil") {
		if _, err := os.Stat(filepath.Join(db, "cert9.db")); err == nil {
			if out, err := run("certutil", "-L", "-d", "sql:"+db, "-n", m.CommonName); err == nil &&
				strings.Contains(out, m.CommonName) {
				return Status{Installed: true, Detail: "NSS database (Chrome/Chromium)"}, nil
			}
		}
	}
	if data, err := os.ReadFile(systemCertFile); err == nil && len(data) > 0 {
		return Status{Installed: true, Detail: "system CA store for this machine"}, nil
	}
	return Status{Installed: false, Detail: "not installed for this user"}, nil
}

func platformInstall(m *Manager, system bool) (string, error) {
	if system {
		cmd := systemInstallCommand(m.CertPath)
		if openTerminal(cmd) {
			return "a terminal window was opened — approve the password prompt there", nil
		}
		return "", fmt.Errorf("run this yourself: %s", cmd)
	}

	db := nssDB()
	if db == "" {
		return "", ErrUnsupported
	}
	if !hasCommand("certutil") {
		return "", ErrUnsupported
	}
	if _, err := os.Stat(filepath.Join(db, "cert9.db")); err != nil {
		if err := os.MkdirAll(db, 0o700); err != nil {
			return "", err
		}
		if _, err := run("certutil", "-N", "-d", "sql:"+db, "--empty-password"); err != nil {
			return "", err
		}
	}
	if _, err := run("certutil", "-A", "-d", "sql:"+db, "-n", m.CommonName, "-t", "C,,", "-i", m.CertPath); err != nil {
		return "", err
	}
	return "NSS database (Chrome/Chromium). Firefox keeps its own store", nil
}

func platformUninstall(m *Manager, system bool) error {
	if system {
		cmd := systemUninstallCommand()
		if openTerminal(cmd) {
			return nil
		}
		return fmt.Errorf("run this yourself: %s", cmd)
	}
	db := nssDB()
	if db == "" || !hasCommand("certutil") {
		return ErrUnsupported
	}
	if _, err := run("certutil", "-D", "-d", "sql:"+db, "-n", m.CommonName); err != nil {
		if strings.Contains(err.Error(), "not find") {
			return nil
		}
		return err
	}
	return nil
}
