package trust

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// errTimedOut reports that a helper had to be abandoned.
var errTimedOut = errors.New("timed out waiting for authorisation")

const (
	commandTimeout = 90 * time.Second
	// quickTimeout bounds operations that may block on a graphical
	// authorisation dialog. macOS shows one for trust changes when it feels
	// like it, and we must not freeze the UI waiting for a click.
	quickTimeout = 6 * time.Second
)

// run executes a helper quietly and returns its combined output. Trust store
// tools often report failures on stdout, so both streams are kept.
func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := out.String()
	if err != nil {
		msg := firstLine(text)
		if msg == "" {
			msg = err.Error()
		}
		return text, fmt.Errorf("%s: %s", name, msg)
	}
	return text, nil
}

// runQuick behaves like run but gives up quickly, so a pending authorisation
// dialog cannot stall the application.
func runQuick(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), quickTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := out.String()
	if ctx.Err() == context.DeadlineExceeded {
		return text, errTimedOut
	}
	if err != nil {
		msg := firstLine(text)
		if msg == "" {
			msg = err.Error()
		}
		return text, fmt.Errorf("%s: %s", name, msg)
	}
	return text, nil
}

// runDetached starts a helper that opens its own window and does not wait for
// it. Used to hand a password prompt to a real terminal.
func runDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// shellQuote makes a path safe to paste into a shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// applescriptQuote renders a string as an AppleScript literal.
func applescriptQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
