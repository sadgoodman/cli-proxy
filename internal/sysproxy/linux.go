//go:build linux

package sysproxy

import (
	"fmt"
	"strconv"
	"strings"
)

func platformName() string { return "linux" }

func platformSupported() bool { return hasCommand("gsettings") }

const gsSchema = "org.gnome.system.proxy"

// gsettingsValue strips the quotes gsettings prints around string values.
func gsettingsValue(schema, key string) (string, error) {
	out, err := run("gsettings", "get", schema, key)
	if err != nil {
		return "", err
	}
	return strings.Trim(strings.TrimSpace(out), "'"), nil
}

func snapshotPlatform() ([]Entry, error) {
	entries := make([]Entry, 0, 5)

	mode, err := gsettingsValue(gsSchema, "mode")
	if err != nil {
		return nil, err
	}
	entries = append(entries, Entry{Scope: "gnome", Key: "mode", Text: mode})

	for _, proto := range []string{"http", "https"} {
		schema := gsSchema + "." + proto
		host, err := gsettingsValue(schema, "host")
		if err != nil {
			return nil, err
		}
		portText, err := gsettingsValue(schema, "port")
		if err != nil {
			return nil, err
		}
		port, _ := strconv.Atoi(portText)
		entries = append(entries, Entry{Scope: "gnome", Key: proto, Server: host, Port: port})
	}
	return entries, nil
}

func applyPlatform(target string, entries []Entry) error {
	host, port := splitTarget(target)
	steps := [][]string{
		{"set", gsSchema, "mode", "manual"},
		{"set", gsSchema + ".http", "host", host},
		{"set", gsSchema + ".http", "port", strconv.Itoa(port)},
		{"set", gsSchema + ".https", "host", host},
		{"set", gsSchema + ".https", "port", strconv.Itoa(port)},
	}
	for _, args := range steps {
		if _, err := run("gsettings", args...); err != nil {
			return err
		}
	}
	return nil
}

func restorePlatform(entries []Entry) error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, e := range entries {
		switch e.Key {
		case "mode":
			mode := e.Text
			if mode == "" {
				mode = "none"
			}
			_, err := run("gsettings", "set", gsSchema, "mode", mode)
			record(err)
		case "http", "https":
			schema := gsSchema + "." + e.Key
			_, err := run("gsettings", "set", schema, "host", e.Server)
			record(err)
			_, err = run("gsettings", "set", schema, "port", strconv.Itoa(e.Port))
			record(err)
		}
	}
	if firstErr != nil {
		return fmt.Errorf("%w (on non-GNOME desktops set the proxy in the desktop settings or export HTTP_PROXY/HTTPS_PROXY)", firstErr)
	}
	return nil
}

func splitTarget(target string) (string, int) {
	host, portText, err := splitHostPort(target)
	if err != nil {
		return target, 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
}
