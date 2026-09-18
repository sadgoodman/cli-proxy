package trust

import (
	"errors"
	"strings"
	"testing"
)

type stub struct {
	available  bool
	status     Status
	checkErr   error
	where      string
	installErr error
	removeErr  error
	systems    []bool
	removes    int
}

func (s *stub) install() {}

func withStub(t *testing.T, avail bool, st Status) (*Manager, *stub) {
	t.Helper()
	s := &stub{available: avail, status: st, where: "somewhere"}
	origAvail, origCheck, origInstall, origUninstall := availableFn, checkFn, installFn, uninstallFn
	availableFn = func() bool { return s.available }
	checkFn = func(*Manager) (Status, error) { return s.status, s.checkErr }
	installFn = func(_ *Manager, system bool) (string, error) {
		s.systems = append(s.systems, system)
		return s.where, s.installErr
	}
	uninstallFn = func(_ *Manager, system bool) error {
		s.systems = append(s.systems, system)
		s.removes++
		return s.removeErr
	}
	t.Cleanup(func() {
		availableFn, checkFn, installFn, uninstallFn = origAvail, origCheck, origInstall, origUninstall
	})
	return New("/tmp/ca.pem", "cli-proxy Root CA"), s
}

func TestManagerPassthrough(t *testing.T) {
	m, s := withStub(t, true, Status{Installed: true, Detail: "macOS keychain"})

	if !m.Available() {
		t.Fatal("Available should follow the platform hook")
	}
	st, err := m.Check()
	if err != nil || !st.Installed || st.Detail != "macOS keychain" {
		t.Fatalf("Check = %+v, %v", st, err)
	}
	where, err := m.Install(false)
	if err != nil || where != "somewhere" {
		t.Fatalf("Install = %q, %v", where, err)
	}
	if len(s.systems) != 1 || s.systems[0] {
		t.Fatalf("install target = %v, want a per-user install", s.systems)
	}
	if err := m.Uninstall(false); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if s.removes != 1 {
		t.Fatal("uninstall did not reach the platform hook")
	}
}

func TestManagerSurfacesErrors(t *testing.T) {
	boom := errors.New("security refused")
	m, s := withStub(t, true, Status{})
	s.installErr = boom
	s.checkErr = boom
	s.removeErr = boom

	if _, err := m.Install(true); !errors.Is(err, boom) {
		t.Fatalf("Install error = %v", err)
	}
	if len(s.systems) != 1 || !s.systems[0] {
		t.Fatalf("system flag not forwarded: %v", s.systems)
	}
	if err := m.Uninstall(true); !errors.Is(err, boom) {
		t.Fatalf("Uninstall error = %v", err)
	}
	if _, err := m.Check(); !errors.Is(err, boom) {
		t.Fatalf("Check error = %v", err)
	}
}

func TestStatusString(t *testing.T) {
	cases := []struct {
		st   Status
		want string
	}{
		{Status{Installed: true}, "trusted"},
		{Status{Installed: true, Detail: "login keychain"}, "trusted — login keychain"},
		{Status{}, "not installed"},
		{Status{Detail: "not trusted by macOS"}, "not trusted by macOS"},
	}
	for _, tc := range cases {
		if got := tc.st.String(); got != tc.want {
			t.Errorf("Status%+v.String() = %q, want %q", tc.st, got, tc.want)
		}
	}
}

func TestTargetDescribesTheScope(t *testing.T) {
	if !strings.Contains(Target(true), "system-wide") {
		t.Fatalf("system target = %q", Target(true))
	}
	if !strings.Contains(Target(false), "this user") {
		t.Fatalf("user target = %q", Target(false))
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("boom\nsecond\n"); got != "boom" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine("  spaced  "); got != "spaced" {
		t.Fatalf("firstLine = %q", got)
	}
}
