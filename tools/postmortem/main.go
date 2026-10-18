// Command postmortem answers one question after a Claude account is revoked:
// which of this fork's measurements looked abnormal in the hours before it.
//
// It reads the service log, not a database: every fact it reports was written by
// the audit trail while the account was still alive.
//
// Usage:
//
//	go run ./tools/postmortem -file /var/log/cliproxyapi.log
//	go run ./tools/postmortem -file service.log -credential ed4c1a2b3f45
//	go run ./tools/postmortem -file service.log -since 48h
//
// The credential flag takes the hashed id printed by the audit trail, which is
// what the logs contain; raw ids are never written.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// profile is one aggregated credential_profile line.
type profile struct {
	Credential string
	At         time.Time
	Requests   int
	Allowed    int
	Blocked    int
	Paced      int
	IPs        int
	UAs        int
	Entry      int
	Sessions   int
	MaxUtil    float64
}

// event is one non-profile audit line.
type event struct {
	Credential string
	At         time.Time
	Event      string
	Decision   string
	Rule       string
	Category   string
	Detail     string
}

func main() {
	path := flag.String("file", "", "service log file")
	credential := flag.String("credential", "", "hashed credential id from the audit trail")
	since := flag.Duration("since", 24*time.Hour, "only consider records newer than this")
	flag.Parse()

	if strings.TrimSpace(*path) == "" {
		fmt.Fprintln(os.Stderr, "postmortem: -file is required")
		os.Exit(2)
	}

	profiles, events, errRead := readLog(*path, time.Now().Add(-*since))
	if errRead != nil {
		fmt.Fprintf(os.Stderr, "postmortem: %v\n", errRead)
		os.Exit(2)
	}

	if *credential != "" {
		profiles = filterProfiles(profiles, *credential)
		events = filterEvents(events, *credential)
	}
	if len(profiles) == 0 && len(events) == 0 {
		fmt.Println("no antidetect records found for that window")
		return
	}

	report(*credential, *since, profiles, events)
}

func readLog(path string, cutoff time.Time) ([]profile, []event, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, nil, errOpen
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "postmortem: close: %v\n", errClose)
		}
	}()

	var profiles []profile
	var events []event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fields, at, ok := parseLine(line)
		if !ok {
			continue
		}
		if !at.IsZero() && at.Before(cutoff) {
			continue
		}
		if fields["event"] == "credential_profile" {
			profiles = append(profiles, parseProfile(fields, at))
			continue
		}
		events = append(events, event{
			Credential: fields["credential"],
			At:         at,
			Event:      fields["event"],
			Decision:   fields["decision"],
			Rule:       fields["rule"],
			Category:   fields["category"],
			Detail:     fields["detail"],
		})
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, nil, errScan
	}
	return profiles, events, nil
}

// parseTimestamp reads the leading "[2006-01-02 15:04:05]" stamp.
func parseTimestamp(line string) time.Time {
	if !strings.HasPrefix(line, "[") {
		return time.Time{}
	}
	end := strings.Index(line, "]")
	if end <= 1 {
		return time.Time{}
	}
	parsed, errParse := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(line[1:end]), time.Local)
	if errParse != nil {
		return time.Time{}
	}
	return parsed
}

// parseLine understands both log shapes this project can emit: the default
// text formatter and logrus JSON output. Deployments switch between them, and a
// post-mortem tool that only reads one of them is useless on the other.
func parseLine(line string) (map[string]string, time.Time, bool) {
	if strings.HasPrefix(strings.TrimSpace(line), "{") {
		return parseJSONLine(line)
	}
	if !strings.Contains(line, "antidetect decision") {
		return nil, time.Time{}, false
	}
	return parseFields(line), parseTimestamp(line), true
}

// parseJSONLine reads one logrus JSON record.
func parseJSONLine(line string) (map[string]string, time.Time, bool) {
	var record map[string]any
	if errUnmarshal := json.Unmarshal([]byte(line), &record); errUnmarshal != nil {
		return nil, time.Time{}, false
	}
	if message, _ := record["msg"].(string); message != "antidetect decision" {
		return nil, time.Time{}, false
	}
	fields := make(map[string]string, len(record))
	for key, value := range record {
		switch typed := value.(type) {
		case string:
			fields[key] = typed
		case float64:
			fields[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			fields[key] = strconv.FormatBool(typed)
		}
	}
	var at time.Time
	if raw, _ := record["time"].(string); raw != "" {
		if parsed, errParse := time.Parse(time.RFC3339Nano, raw); errParse == nil {
			at = parsed
		} else if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
			at = parsed
		}
	}
	return fields, at, true
}

// fieldPattern matches the formatter's two value shapes: quoted free text and
// bare tokens. A hand-rolled scanner missed the key that followed an unquoted
// value, which silently dropped fields from every record.
var fieldPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)="([^"]*)"|([A-Za-z_][A-Za-z0-9_]*)=([^\s]+)`)

// parseFields reads key=value pairs from one log line.
func parseFields(line string) map[string]string {
	fields := make(map[string]string)
	for _, match := range fieldPattern.FindAllStringSubmatch(line, -1) {
		if match[1] != "" {
			fields[match[1]] = match[2]
			continue
		}
		fields[match[3]] = match[4]
	}
	return fields
}

func parseProfile(fields map[string]string, at time.Time) profile {
	return profile{
		Credential: fields["credential"],
		At:         at,
		Requests:   atoi(fields["summary"], "requests"),
		Allowed:    atoi(fields["summary"], "allowed"),
		Blocked:    atoi(fields["summary"], "blocked"),
		Paced:      atoi(fields["summary"], "paced"),
		IPs:        atoi(fields["summary"], "ips"),
		UAs:        atoi(fields["summary"], "uas"),
		Entry:      atoi(fields["summary"], "entry"),
		Sessions:   atoi(fields["summary"], "sessions"),
		MaxUtil:    atof(fields["summary"], "max_util"),
	}
}

// fieldValue pulls one key=value out of the compact summary string.
func fieldValue(summary, key string) string {
	for _, part := range strings.Fields(summary) {
		if strings.HasPrefix(part, key+"=") {
			return strings.TrimPrefix(part, key+"=")
		}
	}
	return ""
}

func atoi(summary, key string) int {
	value, errAtoi := strconv.Atoi(fieldValue(summary, key))
	if errAtoi != nil {
		return 0
	}
	return value
}

func atof(summary, key string) float64 {
	value, errParse := strconv.ParseFloat(fieldValue(summary, key), 64)
	if errParse != nil {
		return 0
	}
	return value
}

func filterProfiles(in []profile, credential string) []profile {
	var out []profile
	for _, item := range in {
		if item.Credential == credential {
			out = append(out, item)
		}
	}
	return out
}

func filterEvents(in []event, credential string) []event {
	var out []event
	for _, item := range in {
		if item.Credential == credential {
			out = append(out, item)
		}
	}
	return out
}

func report(credential string, since time.Duration, profiles []profile, events []event) {
	fmt.Printf("antidetect postmortem · window %s\n", since)
	if credential != "" {
		fmt.Printf("credential: %s\n", credential)
	}
	fmt.Println()

	// Union the profile windows: a credential seen for days has many windows,
	// and the question is what happened across all of them.
	var totalRequests, totalBlocked, totalPaced, maxIPs, maxUAs, maxEntry, maxSessions int
	var maxUtil float64
	for _, item := range profiles {
		totalRequests += item.Requests
		totalBlocked += item.Blocked
		totalPaced += item.Paced
		maxIPs = maxInt(maxIPs, item.IPs)
		maxUAs = maxInt(maxUAs, item.UAs)
		maxEntry = maxInt(maxEntry, item.Entry)
		maxSessions = maxInt(maxSessions, item.Sessions)
		if item.MaxUtil > maxUtil {
			maxUtil = item.MaxUtil
		}
	}

	fmt.Println("profile")
	fmt.Printf("  requests       : %d\n", totalRequests)
	fmt.Printf("  content blocks : %d\n", totalBlocked)
	fmt.Printf("  paced          : %d\n", totalPaced)
	fmt.Printf("  max exits      : %d\n", maxIPs)
	fmt.Printf("  max UAs        : %d\n", maxUAs)
	fmt.Printf("  max entrypoints: %d\n", maxEntry)
	fmt.Printf("  max sessions   : %d\n", maxSessions)
	fmt.Printf("  max window util: %.2f\n", maxUtil)
	fmt.Println()

	fmt.Println("signals (ordered by frequency in observed bans)")
	signals := signalsFrom(totalRequests, totalBlocked, maxIPs, maxUAs, maxEntry, maxUtil)
	if len(signals) == 0 {
		fmt.Println("  none: the measurements this fork takes were all normal")
	}
	for _, item := range signals {
		fmt.Printf("  - %s\n", item)
	}
	fmt.Println()

	byEvent := map[string]int{}
	for _, item := range events {
		byEvent[item.Event+"/"+item.Decision]++
	}
	if len(byEvent) > 0 {
		fmt.Println("decisions")
		keys := make([]string, 0, len(byEvent))
		for key := range byEvent {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  %-32s %d\n", key, byEvent[key])
		}
		fmt.Println()
	}

	if len(events) > 0 {
		fmt.Println("last five decisions")
		start := 0
		if len(events) > 5 {
			start = len(events) - 5
		}
		for _, item := range events[start:] {
			stamp := ""
			if !item.At.IsZero() {
				stamp = item.At.Format("01-02 15:04:05") + " "
			}
			detail := item.Detail
			if item.Rule != "" {
				detail = item.Rule + " " + detail
			}
			fmt.Printf("  %s%s/%s %s\n", stamp, item.Event, item.Decision, strings.TrimSpace(detail))
		}
	}
}

func signalsFrom(requests, blocked, ips, uas, entry int, utilization float64) []string {
	var signals []string
	if ips > 1 {
		signals = append(signals, fmt.Sprintf(
			"multiple exits: up to %d distinct client IPs in one window (sharing signal)", ips))
	}
	if uas > 1 {
		signals = append(signals, fmt.Sprintf(
			"mixed clients: up to %d distinct user agents (third-party clients on one account)", uas))
	}
	if entry > 1 {
		signals = append(signals, fmt.Sprintf(
			"mixed entrypoints: up to %d distinct entrypoints", entry))
	}
	if blocked > 0 {
		signals = append(signals, fmt.Sprintf(
			"content blocks: %d (a safeguards refusal is the one that ends an account)", blocked))
	}
	if utilization >= 0.95 {
		signals = append(signals, fmt.Sprintf(
			"quota window saturated: %.2f", utilization))
	}
	if requests == 0 && (blocked > 0 || utilization > 0) {
		signals = append(signals, "records exist without admitted requests: check the log window")
	}
	return signals
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
