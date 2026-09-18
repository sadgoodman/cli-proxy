//go:build darwin

package trust

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const systemKeychain = "/Library/Keychains/System.keychain"

func platformAvailable() bool { return hasCommand("security") }

// loginKeychain finds the current user's keychain; newer macOS versions use
// the -db suffix.
func loginKeychain() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	for _, name := range []string{"login.keychain-db", "login.keychain"} {
		p := filepath.Join(home, "Library", "Keychains", name)
		if _, statErr := os.Stat(p); statErr == nil {
			return p
		}
	}
	return ""
}

func platformCheck(m *Manager) (Status, error) {
	out, err := run("security", "verify-cert", "-c", m.CertPath)
	// verify-cert prints the verdict on stdout and may still exit non-zero.
	if strings.Contains(out, "verification successful") {
		return Status{Installed: true, Detail: "macOS keychain"}, nil
	}
	if err != nil && !strings.Contains(out, "CSSMERR") {
		return Status{}, err
	}
	return Status{Installed: false, Detail: "not trusted by macOS"}, nil
}

// systemInstallCommand is the elevated command, also shown to the operator
// when no terminal can be opened automatically.
func systemInstallCommand(certPath string) string {
	return "sudo security add-trusted-cert -d -r trustRoot -k " + systemKeychain + " " + shellQuote(certPath)
}

func systemUninstallCommand(commonName string) string {
	return "sudo security remove-trusted-cert -d " + shellQuote(commonName) +
		" ; sudo security delete-certificate -c " + shellQuote(commonName) + " " + systemKeychain
}

// userInstallCommand is the unprivileged command that trusts the root for the
// current user only.
func userInstallCommand(certPath string) string {
	cmd := "security add-trusted-cert -r trustRoot"
	if kc := loginKeychain(); kc != "" {
		cmd += " -k " + shellQuote(kc)
	}
	return cmd + " " + shellQuote(certPath)
}

// openInTerminal runs a command in a new Terminal window, which is where a
// password or approval prompt can be answered without blocking our own input.
func openInTerminal(command string) bool {
	if !hasCommand("osascript") {
		return false
	}
	script := `tell application "Terminal" to do script ` + applescriptQuote(command) + `
tell application "Terminal" to activate`
	_, err := run("osascript", "-e", script)
	return err == nil
}

func platformInstall(m *Manager, system bool) (string, error) {
	if system {
		cmd := systemInstallCommand(m.CertPath)
		if openInTerminal(cmd) {
			return "a Terminal window was opened — approve the password prompt there", nil
		}
		return "", fmt.Errorf("run this yourself: %s", cmd)
	}

	// Usually silent, but macOS may raise an authorisation dialog. Try briefly
	// and hand over to a real terminal if it does.
	if _, err := runQuick("security", "add-trusted-cert", "-r", "trustRoot",
		"-k", loginKeychain(), m.CertPath); err == nil {
		return "macOS login keychain (this user, no password needed)", nil
	}
	if openInTerminal(userInstallCommand(m.CertPath)) {
		return "a Terminal window was opened — approve the request there", nil
	}
	return "", fmt.Errorf("run this yourself: %s", userInstallCommand(m.CertPath))
}

func platformUninstall(m *Manager, system bool) error {
	if system {
		if hasCommand("osascript") {
			script := `tell application "Terminal" to do script ` + applescriptQuote(systemUninstallCommand(m.CommonName)) +
				`
tell application "Terminal" to activate`
			if _, err := run("osascript", "-e", script); err == nil {
				return nil
			}
		}
		return fmt.Errorf("run this yourself: %s", systemUninstallCommand(m.CommonName))
	}
	// Trust setting and keychain entry are separate; drop both.
	_, _ = run("security", "remove-trusted-cert", m.CertPath)
	args := []string{"delete-certificate", "-c", m.CommonName}
	if kc := loginKeychain(); kc != "" {
		args = append(args, kc)
	}
	if _, err := run("security", args...); err != nil {
		if strings.Contains(err.Error(), "could not be found") || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return err
	}
	return nil
}
