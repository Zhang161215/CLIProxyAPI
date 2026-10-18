package content

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// RulesEnv names the file that carries operator-supplied rules.
const RulesEnv = "CPA_ANTIDETECT_CONTENT_RULES"

// ReloadInterval is how often the rule file is checked for changes.
const ReloadInterval = 30 * time.Second

// ruleFileEntry is one rule as written on disk.
type ruleFileEntry struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
}

// LoadRulesFile parses and compiles a rule file.
//
// An invalid pattern fails the whole load: a half-applied rule set is harder to
// reason about than a rejected one, and the caller keeps the previous set.
func LoadRulesFile(path string) ([]Rule, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("content rules: read %s: %w", path, errRead)
	}
	var entries []ruleFileEntry
	if errUnmarshal := json.Unmarshal(raw, &entries); errUnmarshal != nil {
		return nil, fmt.Errorf("content rules: parse %s: %w", path, errUnmarshal)
	}

	rules := make([]Rule, 0, len(entries))
	for index, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			return nil, fmt.Errorf("content rules: entry %d has no id", index)
		}
		category := Category(strings.TrimSpace(entry.Category))
		switch category {
		case CategoryDistill, CategoryJailbreak, CategoryNSFW:
		default:
			return nil, fmt.Errorf("content rules: %s has unknown category %q", id, entry.Category)
		}
		if strings.TrimSpace(entry.Pattern) == "" {
			return nil, fmt.Errorf("content rules: %s has an empty pattern", id)
		}
		compiled, errCompile := regexp.Compile(entry.Pattern)
		if errCompile != nil {
			return nil, fmt.Errorf("content rules: %s: %w", id, errCompile)
		}
		rules = append(rules, Rule{ID: id, Category: category, Pattern: compiled})
	}
	return rules, nil
}

// Watcher reloads the rule file when it changes on disk.
type Watcher struct {
	path     string
	interval time.Duration
	onApply  func(count int)
	onError  func(error)

	mu       sync.Mutex
	lastMod  time.Time
	lastSize int64
}

// NewWatcher builds a watcher for one rule file.
func NewWatcher(path string, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = ReloadInterval
	}
	return &Watcher{path: path, interval: interval}
}

// OnApply registers a callback invoked after a successful reload.
func (w *Watcher) OnApply(fn func(count int)) { w.onApply = fn }

// OnError registers a callback invoked when a reload fails.
func (w *Watcher) OnError(fn func(error)) { w.onError = fn }

// CheckOnce reloads the file if its modification time or size changed.
func (w *Watcher) CheckOnce() error {
	if w == nil || strings.TrimSpace(w.path) == "" {
		return nil
	}
	info, errStat := os.Stat(w.path)
	if errStat != nil {
		return errStat
	}
	w.mu.Lock()
	unchanged := info.ModTime().Equal(w.lastMod) && info.Size() == w.lastSize
	w.mu.Unlock()
	if unchanged {
		return nil
	}

	rules, errLoad := LoadRulesFile(w.path)
	if errLoad != nil {
		if w.onError != nil {
			w.onError(errLoad)
		}
		return errLoad
	}
	SetExtraRules(rules)
	w.mu.Lock()
	w.lastMod = info.ModTime()
	w.lastSize = info.Size()
	w.mu.Unlock()

	if w.onApply != nil {
		w.onApply(len(rules))
	}
	return nil
}

// Run polls the rule file until the context is cancelled.
func (w *Watcher) Run(stop <-chan struct{}) {
	if w == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = w.CheckOnce()
		}
	}
}
