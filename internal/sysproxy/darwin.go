//go:build darwin

package sysproxy

import (
	"fmt"
	"strconv"
	"strings"
)

func platformName() string { return "darwin" }

func platformSupported() bool { return hasCommand("networksetup") }

// networkServices lists the enabled network services. Disabled ones are
// prefixed with an asterisk by networksetup and are skipped: writing to them
// would only slow things down.
func networkServices() ([]string, error) {
	out, err := run("networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	var services []string
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("networksetup reported no enabled network services")
	}
	return services, nil
}

// readProxy parses "networksetup -getwebproxy <service>".
func readProxy(flag, service string) (Entry, error) {
	out, err := run("networksetup", flag, service)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Scope: service}
	if flag == "-getsecurewebproxy" {
		e.Key = "secure"
	} else {
		e.Key = "web"
	}
	for _, line := range strings.Split(out, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "enabled":
			e.Enabled = strings.EqualFold(strings.TrimSpace(value), "yes")
		case "server":
			e.Server = strings.TrimSpace(value)
		case "port":
			e.Port, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	return e, nil
}

func snapshotPlatform() ([]Entry, error) {
	services, err := networkServices()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(services)*2)
	for _, svc := range services {
		web, err := readProxy("-getwebproxy", svc)
		if err != nil {
			return nil, err
		}
		secure, err := readProxy("-getsecurewebproxy", svc)
		if err != nil {
			return nil, err
		}
		entries = append(entries, web, secure)
	}
	return entries, nil
}

func applyPlatform(target string, entries []Entry) error {
	host, port := splitTarget(target)
	services, err := networkServices()
	if err != nil {
		return err
	}
	for _, svc := range services {
		if _, err := run("networksetup", "-setwebproxy", svc, host, strconv.Itoa(port)); err != nil {
			return err
		}
		if _, err := run("networksetup", "-setsecurewebproxy", svc, host, strconv.Itoa(port)); err != nil {
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
		var setFlag, stateFlag string
		switch e.Key {
		case "web":
			setFlag, stateFlag = "-setwebproxy", "-setwebproxystate"
		case "secure":
			setFlag, stateFlag = "-setsecurewebproxy", "-setsecurewebproxystate"
		default:
			continue
		}
		// Put the previous server and port back first: -setwebproxy enables the
		// proxy as a side effect, so the on/off state is corrected afterwards.
		// Restoring only the state would leave our address behind in the
		// (disabled) server field.
		if e.Server != "" {
			_, err := run("networksetup", setFlag, e.Scope, e.Server, strconv.Itoa(e.Port))
			record(err)
		}
		state := "off"
		if e.Enabled {
			state = "on"
		}
		_, err := run("networksetup", stateFlag, e.Scope, state)
		record(err)
	}
	return firstErr
}

// splitTarget turns "host:port" into its parts. The port is validated by the
// caller before we get here.
func splitTarget(target string) (string, int) {
	host, portText, err := splitHostPort(target)
	if err != nil {
		return target, 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
}
