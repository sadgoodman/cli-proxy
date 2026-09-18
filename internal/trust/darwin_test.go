//go:build darwin

package trust

import (
	"strings"
	"testing"
)

func TestSystemInstallCommandIsQuotedAndElevated(t *testing.T) {
	cmd := systemInstallCommand("/tmp/with space/ca.pem")
	if !strings.HasPrefix(cmd, "sudo security add-trusted-cert -d -r trustRoot") {
		t.Fatalf("command = %q", cmd)
	}
	if !strings.Contains(cmd, systemKeychain) {
		t.Fatalf("command does not target the system keychain: %q", cmd)
	}
	if !strings.Contains(cmd, `'/tmp/with space/ca.pem'`) {
		t.Fatalf("path is not shell quoted: %q", cmd)
	}
}

func TestUserInstallCommandTargetsTheLoginKeychain(t *testing.T) {
	cmd := userInstallCommand("/tmp/ca.pem")
	if !strings.HasPrefix(cmd, "security add-trusted-cert -r trustRoot") {
		t.Fatalf("command = %q", cmd)
	}
	if strings.Contains(cmd, "sudo") {
		t.Fatalf("the per-user install must not need sudo: %q", cmd)
	}
	if strings.Contains(cmd, "-k /Library") {
		t.Fatalf("the per-user install must not touch the system keychain: %q", cmd)
	}
}

func TestShellQuoteHandlesQuotes(t *testing.T) {
	if got := shellQuote("/tmp/it's here"); !strings.Contains(got, `'\''`) {
		t.Fatalf("shellQuote = %q", got)
	}
}

func TestApplescriptQuoteEscapesBackslashesAndQuotes(t *testing.T) {
	got := applescriptQuote(`say "hi" \ bye`)
	if strings.Contains(got[1:len(got)-1], `"`) && !strings.Contains(got, `\"`) {
		t.Fatalf("quote not escaped: %q", got)
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("not wrapped in quotes: %q", got)
	}
}
