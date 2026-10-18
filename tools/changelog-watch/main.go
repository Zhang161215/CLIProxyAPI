// Command changelog-watch tracks the upstream Claude Code changelog and flags
// entries that affect this fork's wire fingerprint.
//
// The fingerprint constants in this repository are snapshots of one client
// build. When the client changes the billing header, a beta string, the prompt
// cache layout, or the telemetry contract, those snapshots go stale silently.
// This tool turns that silence into a report.
//
// Usage:
//
//	go run ./tools/changelog-watch                 # diff against the cached baseline
//	go run ./tools/changelog-watch -update         # accept the current changelog as the new baseline
//	go run ./tools/changelog-watch -all            # report the whole file, not just new entries
//
// Exit code is 1 when any new entry matched a fingerprint-relevant keyword, so
// it can gate a scheduled job.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const changelogURL = "https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md"

// strongTags identify entries that can invalidate a wire constant this fork
// depends on. A strong hit fails the run, because a human must re-capture.
var strongTags = []struct {
	needle string
	tag    string
}{
	{"user-agent", "P0-1 identity"},
	{"billing header", "P0-1 identity"},
	{"cc_version", "P0-1 identity"},
	{"anthropic-beta", "P0-1 identity"},
	{"claude-cli", "P0-1 identity"},
	{"prompt cache", "protocol: cache layout"},
	{"cache_control", "protocol: cache layout"},
	{"system prompt", "persona template"},
	{"event_logging", "P2-3 observability"},
	{"telemetry", "P2-3 observability"},
	{"subagent", "session layer"},
	{"compaction", "session layer"},
}

// weakTags are worth reading but do not by themselves invalidate a constant:
// they describe behaviour around the wire rather than the wire itself.
var weakTags = []struct {
	needle string
	tag    string
}{
	{"classifier", "P1-1 content gate"},
	{"safety", "P1-1 content gate"},
	{"gateway", "deployment legitimacy"},
	{"third-party", "deployment legitimacy"},
	{"bedrock", "deployment legitimacy"},
	{"vertex", "deployment legitimacy"},
	{"oauth", "credential layer"},
	{"rate limit", "P2-2 quota window"},
	{"usage window", "P2-2 quota window"},
}

type report struct {
	FetchedAt string        `json:"fetched_at"`
	Version   string        `json:"latest_version"`
	NewLines  []string      `json:"new_lines"`
	Hits      []taggedEntry `json:"hits"`
}

type taggedEntry struct {
	Line string   `json:"line"`
	Tags []string `json:"tags"`
	Weak bool     `json:"weak,omitempty"`
}

func main() {
	update := flag.Bool("update", false, "write the fetched changelog as the new baseline")
	all := flag.Bool("all", false, "report every entry, not only new ones")
	cachePath := flag.String("cache", defaultCachePath(), "baseline file path")
	flag.Parse()

	body, errFetch := fetch(changelogURL)
	if errFetch != nil {
		fmt.Fprintf(os.Stderr, "changelog-watch: fetch: %v\n", errFetch)
		os.Exit(2)
	}

	lines := splitEntries(string(body))
	baseline, errRead := os.ReadFile(*cachePath)
	if errRead != nil && !os.IsNotExist(errRead) {
		fmt.Fprintf(os.Stderr, "changelog-watch: read baseline: %v\n", errRead)
		os.Exit(2)
	}
	known := make(map[string]struct{}, len(lines))
	if !*all {
		for _, line := range splitEntries(string(baseline)) {
			known[line] = struct{}{}
		}
	}

	rep := report{FetchedAt: time.Now().UTC().Format(time.RFC3339), Version: latestVersion(string(body))}
	for _, line := range lines {
		if _, seen := known[line]; seen {
			continue
		}
		rep.NewLines = append(rep.NewLines, line)
		if tags := match(line, strongTags); len(tags) > 0 {
			rep.Hits = append(rep.Hits, taggedEntry{Line: line, Tags: tags})
			continue
		}
		if tags := match(line, weakTags); len(tags) > 0 {
			rep.Hits = append(rep.Hits, taggedEntry{Line: line, Tags: tags, Weak: true})
		}
	}

	// Persist before printing: a caller piping stdout into head would otherwise
	// terminate the process before the baseline is written.
	if *update {
		if errWrite := os.MkdirAll(filepath.Dir(*cachePath), 0o755); errWrite != nil {
			fmt.Fprintf(os.Stderr, "changelog-watch: mkdir: %v\n", errWrite)
			os.Exit(2)
		}
		if errWrite := os.WriteFile(*cachePath, body, 0o644); errWrite != nil {
			fmt.Fprintf(os.Stderr, "changelog-watch: write baseline: %v\n", errWrite)
			os.Exit(2)
		}
	}

	printReport(rep)

	if *update {
		fmt.Printf("\nbaseline updated: %s\n", *cachePath)
		// Accepting a baseline is an explicit decision: do not fail the run.
		return
	}

	for _, hit := range rep.Hits {
		if !hit.Weak {
			os.Exit(1)
		}
	}
}

func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, errGet := client.Get(url)
	if errGet != nil {
		return nil, errGet
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "changelog-watch: close body: %v\n", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// splitEntries keeps only changelog bullet lines and strips the leading "- ".
// Headings are reported separately by latestVersion, so they are not entries.
func splitEntries(text string) []string {
	var entries []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if line == "" {
			continue
		}
		entries = append(entries, line)
	}
	return entries
}

// latestVersion returns the first "## x.y.z" heading.
func latestVersion(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "## "))
		}
	}
	return ""
}

func match(line string, table []struct {
	needle string
	tag    string
}) []string {
	lower := strings.ToLower(line)
	seen := make(map[string]struct{})
	var tags []string
	for _, entry := range table {
		if !strings.Contains(lower, entry.needle) {
			continue
		}
		if _, dup := seen[entry.tag]; dup {
			continue
		}
		seen[entry.tag] = struct{}{}
		tags = append(tags, entry.tag)
	}
	sort.Strings(tags)
	return tags
}

func printReport(rep report) {
	fmt.Printf("changelog-watch %s\n", rep.FetchedAt)
	if rep.Version != "" {
		fmt.Printf("latest upstream version: %s\n", rep.Version)
	}
	strong, weak := 0, 0
	for _, hit := range rep.Hits {
		if hit.Weak {
			weak++
		} else {
			strong++
		}
	}
	fmt.Printf("new entries: %d | strong: %d | review: %d\n", len(rep.NewLines), strong, weak)
	for _, hit := range rep.Hits {
		label := "STRONG"
		if hit.Weak {
			label = "review"
		}
		fmt.Printf("[%s] [%s]\n  %s\n", label, strings.Join(hit.Tags, ", "), hit.Line)
	}
}

func defaultCachePath() string {
	if dir, errUserCache := os.UserCacheDir(); errUserCache == nil {
		return filepath.Join(dir, "cliproxyapi-fork", "claude-changelog.md")
	}
	return ".claude-changelog.md"
}
