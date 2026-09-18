package sysproxy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stub swaps the platform hooks for the duration of a test.
func stub(t *testing.T, entries []Entry, applyErr, restoreErr error) (applied *[]string, restored *int) {
	t.Helper()
	appliedList := []string{}
	restoredCount := 0

	origSnapshot, origApply, origRestore := snapshotFn, applyFn, restoreFn
	snapshotFn = func() ([]Entry, error) { return entries, nil }
	applyFn = func(target string, _ []Entry) error {
		if applyErr != nil {
			return applyErr
		}
		appliedList = append(appliedList, target)
		return nil
	}
	restoreFn = func(got []Entry) error {
		restoredCount++
		if restoreErr != nil {
			return restoreErr
		}
		if len(got) != len(entries) {
			t.Errorf("restore got %d entries, want %d", len(got), len(entries))
		}
		return nil
	}
	t.Cleanup(func() {
		snapshotFn, applyFn, restoreFn = origSnapshot, origApply, origRestore
	})
	return &appliedList, &restoredCount
}

func sampleEntries() []Entry {
	return []Entry{
		{Scope: "Wi-Fi", Key: "web", Enabled: true, Server: "10.0.0.1", Port: 3128},
		{Scope: "Wi-Fi", Key: "secure", Enabled: false},
	}
}

func TestLocalTarget(t *testing.T) {
	cases := []struct {
		addr     string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"0.0.0.0:8080", "127.0.0.1", 8080, false},
		{":8080", "127.0.0.1", 8080, false},
		{"[::]:9090", "127.0.0.1", 9090, false},
		{"127.0.0.1:1234", "127.0.0.1", 1234, false},
		{"192.168.3.15:8080", "192.168.3.15", 8080, false},
		{"127.0.0.1", "", 0, true},
		{"0.0.0.0:0", "", 0, true},
		{"0.0.0.0:70000", "", 0, true},
		{"0.0.0.0:abc", "", 0, true},
	}
	for _, tc := range cases {
		host, port, err := LocalTarget(tc.addr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("LocalTarget(%q) should fail, got %s:%d", tc.addr, host, port)
			}
			continue
		}
		if err != nil {
			t.Errorf("LocalTarget(%q): %v", tc.addr, err)
			continue
		}
		if host != tc.wantHost || port != tc.wantPort {
			t.Errorf("LocalTarget(%q) = %s:%d, want %s:%d", tc.addr, host, port, tc.wantHost, tc.wantPort)
		}
	}
}

func TestEnableAndRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	applied, restored := stub(t, sampleEntries(), nil, nil)

	c := New(dir, nil)
	if c.Active() {
		t.Fatal("controller should start idle")
	}
	if err := c.Enable("127.0.0.1", 8080); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !c.Active() {
		t.Fatal("controller should be active after Enable")
	}
	if c.Target() != "127.0.0.1:8080" {
		t.Fatalf("target = %q", c.Target())
	}
	if len(*applied) != 1 || (*applied)[0] != "127.0.0.1:8080" {
		t.Fatalf("applied = %v", *applied)
	}
	if _, err := os.Stat(filepath.Join(dir, backupFile)); err != nil {
		t.Fatalf("recovery copy was not written: %v", err)
	}

	if err := c.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if c.Active() {
		t.Fatal("controller should be idle after Restore")
	}
	if *restored != 1 {
		t.Fatalf("restore called %d times, want 1", *restored)
	}
	if _, err := os.Stat(filepath.Join(dir, backupFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery copy should be removed after a successful restore")
	}

	// Restoring again must be a harmless no-op.
	if err := c.Restore(); err != nil {
		t.Fatalf("second Restore: %v", err)
	}
	if *restored != 1 {
		t.Fatalf("restore ran again with nothing to undo: %d", *restored)
	}
}

func TestEnableRepointsWhenAlreadyActive(t *testing.T) {
	applied, _ := stub(t, sampleEntries(), nil, nil)
	c := New(t.TempDir(), nil)

	if err := c.EnableAddr("0.0.0.0:8080"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnableAddr("0.0.0.0:9090"); err != nil {
		t.Fatal(err)
	}
	if c.Target() != "127.0.0.1:9090" {
		t.Fatalf("target = %q, want the new port", c.Target())
	}
	if len(*applied) != 2 {
		t.Fatalf("applied %v, want two calls", *applied)
	}
	// Re-pointing must not re-snapshot: the original configuration is what
	// has to be restored.
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
}

func TestEnableRollsBackWhenApplyFails(t *testing.T) {
	dir := t.TempDir()
	_, restored := stub(t, sampleEntries(), errors.New("networksetup exploded"), nil)

	c := New(dir, nil)
	err := c.Enable("127.0.0.1", 8080)
	if err == nil {
		t.Fatal("Enable should report the failure")
	}
	if c.Active() {
		t.Fatal("a failed Enable must not leave the controller active")
	}
	if *restored != 1 {
		t.Fatalf("rollback ran %d times, want 1", *restored)
	}
	if _, statErr := os.Stat(filepath.Join(dir, backupFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("the recovery copy should be removed after a rollback")
	}
}

func TestRecoverStaleUndoesAKilledRun(t *testing.T) {
	dir := t.TempDir()
	entries := sampleEntries()

	// Simulate a run that enabled the proxy and was killed before restoring.
	crashed := New(dir, nil)
	origSnapshot := snapshotFn
	snapshotFn = func() ([]Entry, error) { return entries, nil }
	if err := crashed.writeBackup(&Snapshot{
		Platform: platformName(),
		Target:   "127.0.0.1:8080",
		Entries:  entries,
	}); err != nil {
		t.Fatal(err)
	}
	snapshotFn = origSnapshot

	_, restored := stub(t, entries, nil, nil)
	fresh := New(dir, nil)
	if !fresh.RecoverStale() {
		t.Fatal("RecoverStale should report that it cleaned something up")
	}
	if *restored != 1 {
		t.Fatalf("restore ran %d times, want 1", *restored)
	}
	if _, err := os.Stat(filepath.Join(dir, backupFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stale recovery copy should be gone")
	}
	// Nothing to do the second time.
	if fresh.RecoverStale() {
		t.Fatal("RecoverStale should be a no-op when there is no backup")
	}
}

func TestRestoreKeepsTheBackupWhenItFails(t *testing.T) {
	dir := t.TempDir()
	stub(t, sampleEntries(), nil, errors.New("networksetup refused"))

	c := New(dir, nil)
	if err := c.Enable("127.0.0.1", 8080); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(); err == nil {
		t.Fatal("Restore should report the failure")
	}
	if _, err := os.Stat(filepath.Join(dir, backupFile)); err != nil {
		t.Fatal("the recovery copy must survive so a later run can retry")
	}
}

func TestBackupFromAnotherPlatformIsIgnored(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, nil)
	if err := os.WriteFile(filepath.Join(dir, backupFile),
		[]byte(`{"platform":"plan9","entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.RecoverStale() {
		t.Fatal("a backup from another platform must be ignored")
	}
}

func TestControllerWithUnsupportedPlatformIsInert(t *testing.T) {
	if platformSupported() {
		t.Skip("this platform does support system proxy control")
	}
	c := New(t.TempDir(), nil)
	if c.Supported() {
		t.Fatal("Supported should be false")
	}
	if err := c.Enable("127.0.0.1", 8080); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Enable = %v, want ErrUnsupported", err)
	}
}
