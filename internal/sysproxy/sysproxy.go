// Package sysproxy points the operating system's own proxy settings at
// cli-proxy and then puts them back exactly as they were.
//
// Enabling is always explicit, the previous configuration is snapshotted both
// in memory and on disk, and the on-disk copy lets a later run clean up after
// a crash (SIGKILL, power loss) that never got to restore anything.
package sysproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnsupported is returned when the current platform has no implementation.
var ErrUnsupported = errors.New("system proxy control is not implemented on this platform")

// backupFile is where the previous configuration is parked so that an
// interrupted run can be undone later.
const backupFile = "sysproxy-backup.json"

// Entry is one restorable piece of the system proxy configuration. The exact
// meaning of Scope/Key/Text differs per platform:
//
//	darwin   Scope = network service, Key = "web" | "secure"
//	linux    Scope = "gnome",         Key = "mode" | "http" | "https"
//	windows  Scope = "wininet",       Key = registry value name
type Entry struct {
	Scope   string `json:"scope,omitempty"`
	Key     string `json:"key,omitempty"`
	Enabled bool   `json:"enabled"`
	Server  string `json:"server,omitempty"`
	Port    int    `json:"port,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Snapshot is the complete previous configuration.
type Snapshot struct {
	Platform string    `json:"platform"`
	Target   string    `json:"target"`
	Entries  []Entry   `json:"entries"`
	TakenAt  time.Time `json:"taken_at"`
	PID      int       `json:"pid"`
}

// Indirect hooks so tests can exercise the lifecycle without touching the
// real machine configuration.
var (
	snapshotFn = snapshotPlatform
	applyFn    = applyPlatform
	restoreFn  = restorePlatform
)

// Controller owns the enable/restore lifecycle.
type Controller struct {
	dir  string
	logf func(string, ...any)

	mu     sync.Mutex
	snap   *Snapshot
	target string
}

// New creates a controller that keeps its crash-recovery copy in dir.
func New(dir string, logf func(string, ...any)) *Controller {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Controller{dir: dir, logf: logf}
}

// Supported reports whether this platform can drive the system proxy.
func Supported() bool { return platformSupported() }

// Supported is the method form, so a controller satisfies UI interfaces.
func (c *Controller) Supported() bool { return Supported() }

// EnableAddr points the system proxy at a listen address such as
// "0.0.0.0:8080", mapping wildcard hosts to loopback.
func (c *Controller) EnableAddr(addr string) error {
	host, port, err := LocalTarget(addr)
	if err != nil {
		return err
	}
	return c.Enable(host, port)
}

// Active reports whether the system proxy currently points at cli-proxy.
func (c *Controller) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap != nil
}

// Target returns the host:port the system proxy is pointed at, if active.
func (c *Controller) Target() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

// LocalTarget converts a listen address into the host and port the system
// proxy should use. Wildcards become the loopback address.
func LocalTarget(addr string) (string, int, error) {
	host, portText, err := splitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in %q", addr)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return host, port, nil
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("address %q has no port", addr)
	}
	host := strings.Trim(addr[:i], "[]")
	return host, addr[i+1:], nil
}

// Enable snapshots the current configuration and points the system proxy at
// host:port. Calling it while already active simply re-points the proxy.
func (c *Controller) Enable(host string, port int) error {
	if host == "" {
		host = "127.0.0.1"
	}
	target := host + ":" + strconv.Itoa(port)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.snap != nil {
		return c.repointLocked(host, port, target)
	}

	entries, err := snapshotFn()
	if err != nil {
		return err
	}
	snap := &Snapshot{
		Platform: platformName(),
		Target:   target,
		Entries:  entries,
		TakenAt:  time.Now(),
		PID:      os.Getpid(),
	}
	// Park the snapshot before touching anything, so an interrupted enable can
	// still be undone by the next run.
	if err := c.writeBackup(snap); err != nil {
		return fmt.Errorf("cannot save the previous proxy settings: %w", err)
	}
	if err := applyFn(target, entries); err != nil {
		// Best effort: put back whatever we already changed.
		if rerr := restoreFn(entries); rerr != nil {
			c.logf("system proxy: rollback failed: %v", rerr)
		}
		c.removeBackup()
		return err
	}
	c.snap = snap
	c.target = target
	c.logf("system proxy set to %s", target)
	return nil
}

func (c *Controller) repointLocked(host string, port int, target string) error {
	if c.target == target {
		return nil
	}
	if err := applyFn(target, c.snap.Entries); err != nil {
		return err
	}
	c.target = target
	c.snap.Target = target
	if err := c.writeBackup(c.snap); err != nil {
		c.logf("system proxy: cannot update the recovery copy: %v", err)
	}
	c.logf("system proxy moved to %s", target)
	return nil
}

// Disable restores the previous configuration and forgets the snapshot.
func (c *Controller) Disable() error { return c.Restore() }

// Restore puts the saved configuration back. It is safe to call repeatedly and
// from a defer; a no-op when nothing was changed.
func (c *Controller) Restore() error {
	c.mu.Lock()
	snap := c.snap
	c.snap = nil
	target := c.target
	c.target = ""
	c.mu.Unlock()

	if snap == nil {
		// Nothing enabled in this process; make sure no stale backup is left.
		c.removeBackup()
		return nil
	}
	if err := restoreFn(snap.Entries); err != nil {
		// Keep the backup so a later run can try again.
		return fmt.Errorf("cannot restore the system proxy: %w", err)
	}
	c.removeBackup()
	if target != "" {
		c.logf("system proxy restored (was %s)", target)
	}
	return nil
}

// RecoverStale undoes a configuration left behind by a run that was killed
// before it could restore. Call it once at startup.
func (c *Controller) RecoverStale() bool {
	snap, err := c.readBackup()
	if err != nil {
		return false
	}
	if err := restoreFn(snap.Entries); err != nil {
		c.logf("system proxy: could not undo a leftover configuration from pid %d: %v", snap.PID, err)
		return false
	}
	c.removeBackup()
	c.logf("system proxy: restored settings left over from a previous run (pid %d, %s)",
		snap.PID, snap.TakenAt.Format(time.RFC3339))
	return true
}

// Describe renders the configuration for the UI.
func (c *Controller) Describe() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap == nil {
		return "off"
	}
	return "on -> " + c.target
}

// ---------------------------------------------------------------------------
// backup persistence
// ---------------------------------------------------------------------------

func (c *Controller) backupPath() string {
	if c.dir == "" {
		return ""
	}
	return filepath.Join(c.dir, backupFile)
}

func (c *Controller) writeBackup(snap *Snapshot) error {
	path := c.backupPath()
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Controller) readBackup() (*Snapshot, error) {
	path := c.backupPath()
	if path == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	if snap.Platform != platformName() {
		return nil, fmt.Errorf("backup is for %s, not %s", snap.Platform, platformName())
	}
	return &snap, nil
}

func (c *Controller) removeBackup() {
	if path := c.backupPath(); path != "" {
		_ = os.Remove(path)
	}
}
