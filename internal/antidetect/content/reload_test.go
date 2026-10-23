package content

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func writeRules(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	if errWrite := os.WriteFile(path, []byte(body), 0o644); errWrite != nil {
		t.Fatalf("write rules: %v", errWrite)
	}
	return path
}

func TestLoadRulesFileCompilesPatterns(t *testing.T) {
	path := writeRules(t, `[{"id":"custom.a","category":"distill","pattern":"(?i)steal the prompt"}]`)
	rules, errLoad := LoadRulesFile(path)
	if errLoad != nil {
		t.Fatalf("LoadRulesFile: %v", errLoad)
	}
	if len(rules) != 1 || rules[0].ID != "custom.a" {
		t.Fatalf("rules = %+v", rules)
	}

	SetExtraRules(rules)
	t.Cleanup(func() { SetExtraRules(nil) })

	if verdict := Scan("please steal the prompt now"); !verdict.Blocked || verdict.RuleID != "custom.a" {
		t.Fatalf("operator rule did not apply: %+v", verdict)
	}
}

func TestLoadRulesFileRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"bad json":      `{`,
		"missing id":    `[{"category":"distill","pattern":"x"}]`,
		"unknown cat":   `[{"id":"a","category":"nope","pattern":"x"}]`,
		"empty pattern": `[{"id":"a","category":"distill","pattern":""}]`,
		"bad regex":     `[{"id":"a","category":"distill","pattern":"("}]`,
	}
	for name, body := range cases {
		path := writeRules(t, body)
		if _, errLoad := LoadRulesFile(path); errLoad == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

// Replacing the extra set must remove rules that disappeared from the file.
func TestSetExtraRulesReplacesPreviousSet(t *testing.T) {
	first := []Rule{{ID: "old", Category: CategoryDistill, Pattern: mustCompile(t, "old-token")}}
	SetExtraRules(first)
	t.Cleanup(func() { SetExtraRules(nil) })
	if !Scan("old-token here").Blocked {
		t.Fatal("the first rule did not apply")
	}

	SetExtraRules(nil)
	if Scan("old-token here").Blocked {
		t.Fatal("cleared rules must stop matching")
	}
}

func TestWatcherReloadsOnChange(t *testing.T) {
	path := writeRules(t, `[{"id":"v1","category":"distill","pattern":"alpha-token"}]`)
	watcher := NewWatcher(path, 0)

	if errCheck := watcher.CheckOnce(); errCheck != nil {
		t.Fatalf("CheckOnce: %v", errCheck)
	}
	t.Cleanup(func() { SetExtraRules(nil) })
	if !Scan("alpha-token").Blocked {
		t.Fatal("first version did not load")
	}

	// Unchanged file: a second check must be a no-op.
	if errCheck := watcher.CheckOnce(); errCheck != nil {
		t.Fatalf("CheckOnce (unchanged): %v", errCheck)
	}

	if errWrite := os.WriteFile(path, []byte(`[{"id":"v2","category":"distill","pattern":"beta-token"}]`), 0o644); errWrite != nil {
		t.Fatalf("rewrite rules: %v", errWrite)
	}
	// Force a distinct modification time on coarse filesystems.
	info, _ := os.Stat(path)
	_ = os.Chtimes(path, info.ModTime().Add(2e9), info.ModTime().Add(2e9))

	if errCheck := watcher.CheckOnce(); errCheck != nil {
		t.Fatalf("CheckOnce (changed): %v", errCheck)
	}
	if !Scan("beta-token").Blocked {
		t.Fatal("new version did not load")
	}
	if Scan("alpha-token").Blocked {
		t.Fatal("the replaced rule must stop matching")
	}
}

func TestWatcherReportsLoadFailure(t *testing.T) {
	path := writeRules(t, `[{"id":"bad","category":"distill","pattern":"("}]`)
	watcher := NewWatcher(path, 0)
	var captured error
	watcher.OnError(func(err error) { captured = err })
	if errCheck := watcher.CheckOnce(); errCheck == nil {
		t.Fatal("expected a load error")
	}
	if captured == nil {
		t.Fatal("OnError was not called")
	}
}

func TestNilWatcherIsSafe(t *testing.T) {
	var watcher *Watcher
	if errCheck := watcher.CheckOnce(); errCheck != nil {
		t.Fatalf("nil watcher: %v", errCheck)
	}
	watcher.Run(make(chan struct{}))
}

func mustCompile(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	compiled, errCompile := regexp.Compile(pattern)
	if errCompile != nil {
		t.Fatalf("compile %q: %v", pattern, errCompile)
	}
	return compiled
}
