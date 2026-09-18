//go:build windows

package sysproxy

import (
	"errors"
	"strconv"
	"strings"
	"syscall"
)

func platformName() string { return "windows" }

func platformSupported() bool { return hasCommand("reg") }

const inetKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// regValues we snapshot and restore.
var winValues = []string{"ProxyEnable", "ProxyServer", "ProxyOverride"}

func regQuery(name string) (string, bool) {
	out, err := run("reg", "query", inetKey, "/v", name)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.EqualFold(fields[0], name) {
			return strings.Join(fields[2:], " "), true
		}
	}
	return "", false
}

func regSet(name, typ, value string) error {
	_, err := run("reg", "add", inetKey, "/v", name, "/t", typ, "/d", value, "/f")
	return err
}

func snapshotPlatform() ([]Entry, error) {
	entries := make([]Entry, 0, len(winValues))
	for _, name := range winValues {
		value, ok := regQuery(name)
		entries = append(entries, Entry{Scope: "wininet", Key: name, Enabled: ok, Text: value})
	}
	return entries, nil
}

func applyPlatform(target string, entries []Entry) error {
	if err := regSet("ProxyEnable", "REG_DWORD", "1"); err != nil {
		return err
	}
	if err := regSet("ProxyServer", "REG_SZ", target); err != nil {
		return err
	}
	refreshInternetSettings()
	return nil
}

func restorePlatform(entries []Entry) error {
	var firstErr error
	for _, e := range entries {
		var err error
		switch {
		case e.Key == "ProxyEnable":
			value := "0"
			if e.Enabled && (e.Text == "0x1" || e.Text == "1") {
				value = "1"
			}
			err = regSet("ProxyEnable", "REG_DWORD", value)
		case e.Enabled:
			err = regSet(e.Key, "REG_SZ", e.Text)
		default:
			// The value did not exist before; remove it again.
			_, err = run("reg", "delete", inetKey, "/v", e.Key, "/f")
			if err != nil && strings.Contains(err.Error(), "unable to find") {
				err = nil
			}
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	refreshInternetSettings()
	return firstErr
}

// refreshInternetSettings tells WinINet that the registry changed, otherwise
// the new proxy only takes effect after a restart.
func refreshInternetSettings() {
	const (
		internetOptionSettingsChanged = 39
		internetOptionRefresh         = 37
	)
	wininet := syscall.NewLazyDLL("wininet.dll")
	proc := wininet.NewProc("InternetSetOptionW")
	if err := proc.Find(); err != nil {
		return
	}
	_, _, _ = proc.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = proc.Call(0, internetOptionRefresh, 0, 0)
}

func splitTarget(target string) (string, int) {
	host, portText, err := splitHostPort(target)
	if err != nil {
		return target, 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
}

var _ = errors.New
