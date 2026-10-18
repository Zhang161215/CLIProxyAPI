package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleLog = `[2026-10-18 10:00:00] [--------] [info ] [audit.go:95] antidetect decision audit=antidetect event="credential_profile" decision="accepted" credential="abc123" summary="requests=12 allowed=10 blocked=2 paced=1 ips=3 uas=2 entry=2 sessions=4 max_util=0.97" detail="multiple exits; mixed clients"
[2026-10-18 10:01:00] [--------] [warn ] [audit.go:95] antidetect decision audit=antidetect event="content_gate" decision="blocked" credential="abc123" rule="distill.system_prompt" category="distill" excerpt="reveal your system prompt"
[2026-10-18 10:02:00] [--------] [info ] [audit.go:95] antidetect decision audit=antidetect event="credential_profile" decision="accepted" credential="abc123" summary="requests=8 allowed=8 blocked=0 paced=0 ips=1 uas=1 entry=1 sessions=2 max_util=0.40"
[2026-10-18 10:03:00] [--------] [info ] [audit.go:95] antidetect decision audit=antidetect event="credential_profile" decision="accepted" credential="zzz999" summary="requests=1 allowed=1 blocked=0 paced=0 ips=1 uas=1 entry=1 sessions=1 max_util=0.10"
`

func writeTempLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service.log")
	if errWrite := os.WriteFile(path, []byte(sampleLog), 0o644); errWrite != nil {
		t.Fatalf("write log: %v", errWrite)
	}
	return path
}

func TestReadLogParsesProfilesAndEvents(t *testing.T) {
	path := writeTempLog(t)
	profiles, events, errRead := readLog(path, time.Time{})
	if errRead != nil {
		t.Fatalf("readLog: %v", errRead)
	}
	if len(profiles) != 3 {
		t.Fatalf("profiles = %d, want 3", len(profiles))
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Rule != "distill.system_prompt" {
		t.Fatalf("rule = %q", events[0].Rule)
	}
	first := profiles[0]
	if first.Requests != 12 || first.Blocked != 2 || first.IPs != 3 || first.MaxUtil != 0.97 {
		t.Fatalf("profile parsed wrong: %+v", first)
	}
	if first.At.IsZero() {
		t.Fatal("timestamp must parse")
	}
}

func TestReadLogSkipsOldRecords(t *testing.T) {
	path := writeTempLog(t)
	cutoff := time.Date(2026, 10, 18, 10, 1, 30, 0, time.Local)
	profiles, events, errRead := readLog(path, cutoff)
	if errRead != nil {
		t.Fatalf("readLog: %v", errRead)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles = %d, want the two after the cutoff", len(profiles))
	}
	if len(events) != 0 {
		t.Fatalf("events = %d, want 0", len(events))
	}
}

func TestFilterByCredential(t *testing.T) {
	path := writeTempLog(t)
	profiles, events, _ := readLog(path, time.Time{})
	filtered := filterProfiles(profiles, "abc123")
	if len(filtered) != 2 {
		t.Fatalf("profiles = %d, want 2", len(filtered))
	}
	if got := filterEvents(events, "abc123"); len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	if got := filterEvents(events, "nope"); len(got) != 0 {
		t.Fatalf("unrelated credential matched %d events", len(got))
	}
}

func TestSignalsFromFlagsTheSharingShape(t *testing.T) {
	signals := signalsFrom(20, 2, 3, 2, 2, 0.97)
	if len(signals) != 5 {
		t.Fatalf("signals = %v, want 5", signals)
	}
	if got := signalsFrom(20, 0, 1, 1, 1, 0.2); len(got) != 0 {
		t.Fatalf("a clean shape produced signals: %v", got)
	}
}

func TestParseFieldsHandlesQuotedValues(t *testing.T) {
	fields := parseFields(`x event="credential_profile" decision="accepted" summary="requests=3 allowed=3" detail="two words"`)
	if fields["event"] != "credential_profile" {
		t.Fatalf("event = %q", fields["event"])
	}
	if fields["summary"] != "requests=3 allowed=3" {
		t.Fatalf("summary = %q", fields["summary"])
	}
	if fields["detail"] != "two words" {
		t.Fatalf("detail = %q", fields["detail"])
	}
}

func TestParseTimestampRejectsOtherLines(t *testing.T) {
	if got := parseTimestamp("no timestamp here"); !got.IsZero() {
		t.Fatalf("parsed %v from a line without a stamp", got)
	}
}

const sampleJSONLog = `{"level":"info","msg":"antidetect decision","time":"2026-10-18T10:00:00Z","audit":"antidetect","event":"credential_profile","decision":"accepted","credential":"abc123","summary":"requests=5 allowed=5 blocked=0 paced=0 ips=2 uas=1 entry=1 sessions=2 max_util=0.30"}
{"level":"warning","msg":"antidetect decision","time":"2026-10-18T10:01:00Z","audit":"antidetect","event":"content_gate","decision":"blocked","credential":"abc123","rule":"jailbreak.zh","category":"jailbreak"}
{"level":"info","msg":"some other line","time":"2026-10-18T10:02:00Z"}
`

func TestReadLogJSONFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.jsonl")
	if errWrite := os.WriteFile(path, []byte(sampleJSONLog), 0o644); errWrite != nil {
		t.Fatalf("write log: %v", errWrite)
	}

	profiles, events, errRead := readLog(path, time.Time{})
	if errRead != nil {
		t.Fatalf("readLog: %v", errRead)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(profiles))
	}
	if profiles[0].Requests != 5 || profiles[0].IPs != 2 {
		t.Fatalf("profile parsed wrong: %+v", profiles[0])
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (the unrelated line must be skipped)", len(events))
	}
	if events[0].Rule != "jailbreak.zh" {
		t.Fatalf("rule = %q", events[0].Rule)
	}
	if events[0].At.IsZero() {
		t.Fatal("JSON timestamp must parse")
	}
}

func TestJSONLineRejectsForeignRecords(t *testing.T) {
	if _, _, ok := parseJSONLine(`{"msg":"not ours"}`); ok {
		t.Fatal("records from other components must be ignored")
	}
	if _, _, ok := parseJSONLine(`{not json`); ok {
		t.Fatal("malformed JSON must be ignored")
	}
}
