package core

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// LogLine is a single timestamped engine message.
type LogLine struct {
	At   time.Time
	Text string
}

// Logger is a small bounded ring buffer of engine messages with change
// notification, so the UI can show proxy activity without a real log file.
type Logger struct {
	mu     sync.Mutex
	lines  []LogLine
	max    int
	notify chan struct{}
}

// NewLogger creates a logger retaining at most max lines.
func NewLogger(max int) *Logger {
	if max <= 0 {
		max = 200
	}
	return &Logger{max: max, notify: make(chan struct{}, 1)}
}

// Addf appends a formatted message.
func (l *Logger) Addf(format string, args ...any) {
	if l == nil {
		return
	}
	text := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
	l.mu.Lock()
	l.lines = append(l.lines, LogLine{At: time.Now(), Text: text})
	if len(l.lines) > l.max {
		l.lines = append(l.lines[:0], l.lines[len(l.lines)-l.max:]...)
	}
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

// Tail returns the most recent n lines, oldest first.
func (l *Logger) Tail(n int) []LogLine {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.lines) {
		n = len(l.lines)
	}
	out := make([]LogLine, n)
	copy(out, l.lines[len(l.lines)-n:])
	return out
}

// Changes returns a channel signalled when a line is appended.
func (l *Logger) Changes() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.notify
}
