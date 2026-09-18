package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Filter is a saved display filter. Enabled filters are ANDed together and
// applied to every flow, on top of the ad-hoc filter typed in the filter bar.
type Filter struct {
	ID      int    `json:"id"`
	Enabled bool   `json:"enabled"`
	Expr    string `json:"expr"`
	Note    string `json:"note,omitempty"`
}

// FilterSet is a concurrency-safe, persisted collection of saved filters.
type FilterSet struct {
	mu       sync.RWMutex
	filters  []*Filter
	nextID   int
	path     string
	compiled []*Query
}

// NewFilterSet creates an empty set optionally backed by a JSON file.
func NewFilterSet(path string) *FilterSet {
	return &FilterSet{path: path}
}

// Path returns the backing file location.
func (fs *FilterSet) Path() string { return fs.path }

// List returns copies of the filters.
func (fs *FilterSet) List() []Filter {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	out := make([]Filter, len(fs.filters))
	for i, f := range fs.filters {
		out[i] = *f
	}
	return out
}

// Len returns how many filters are stored.
func (fs *FilterSet) Len() int {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return len(fs.filters)
}

// Add stores a new filter with the given expression.
func (fs *FilterSet) Add(expr, note string) *Filter {
	fs.mu.Lock()
	fs.nextID++
	f := &Filter{ID: fs.nextID, Enabled: true, Expr: expr, Note: note}
	fs.filters = append(fs.filters, f)
	fs.mu.Unlock()
	_ = fs.Save()
	return f
}

// Replace swaps the filter with the same id.
func (fs *FilterSet) Replace(f *Filter) {
	fs.mu.Lock()
	for i, old := range fs.filters {
		if old.ID == f.ID {
			fs.filters[i] = f
			break
		}
	}
	fs.mu.Unlock()
	_ = fs.Save()
}

// Remove deletes the filter with the given id.
func (fs *FilterSet) Remove(id int) {
	fs.mu.Lock()
	for i, f := range fs.filters {
		if f.ID == id {
			fs.filters = append(fs.filters[:i], fs.filters[i+1:]...)
			break
		}
	}
	fs.mu.Unlock()
	_ = fs.Save()
}

// Toggle flips the enabled flag and returns the new value.
func (fs *FilterSet) Toggle(id int) bool {
	fs.mu.Lock()
	var enabled bool
	for _, f := range fs.filters {
		if f.ID == id {
			f.Enabled = !f.Enabled
			enabled = f.Enabled
			break
		}
	}
	fs.mu.Unlock()
	_ = fs.Save()
	return enabled
}

// Clear removes every filter.
func (fs *FilterSet) Clear() {
	fs.mu.Lock()
	fs.filters = nil
	fs.mu.Unlock()
	_ = fs.Save()
}

// EnabledExprs lists the expressions of the enabled filters.
func (fs *FilterSet) EnabledExprs() []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	var out []string
	for _, f := range fs.filters {
		if f.Enabled && strings.TrimSpace(f.Expr) != "" {
			out = append(out, f.Expr)
		}
	}
	return out
}

// Match reports whether a flow satisfies every enabled saved filter.
func (fs *FilterSet) Match(f *Flow) bool {
	qs := fs.queries()
	for _, q := range qs {
		if !q.Match(f) {
			return false
		}
	}
	return true
}

func (fs *FilterSet) queries() []*Query {
	fs.mu.RLock()
	if fs.compiled != nil {
		defer fs.mu.RUnlock()
		return fs.compiled
	}
	var exprs []string
	for _, f := range fs.filters {
		if f.Enabled && strings.TrimSpace(f.Expr) != "" {
			exprs = append(exprs, f.Expr)
		}
	}
	fs.mu.RUnlock()

	compiled := make([]*Query, 0, len(exprs))
	for _, e := range exprs {
		compiled = append(compiled, CompileQuery(e))
	}

	fs.mu.Lock()
	fs.compiled = compiled
	fs.mu.Unlock()
	return compiled
}

// Load reads filters from the backing file. A missing file is not an error.
func (fs *FilterSet) Load() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.path == "" {
		return nil
	}
	data, err := os.ReadFile(fs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var filters []*Filter
	if err := json.Unmarshal(data, &filters); err != nil {
		return err
	}
	fs.filters = filters
	fs.compiled = nil
	for _, f := range fs.filters {
		if f.ID > fs.nextID {
			fs.nextID = f.ID
		}
	}
	return nil
}

// Save writes the filters to the backing file.
func (fs *FilterSet) Save() error {
	fs.mu.Lock()
	fs.compiled = nil
	path := fs.path
	data, err := json.MarshalIndent(fs.filters, "", "  ")
	fs.mu.Unlock()
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
