package config

import (
	"os"
	"path/filepath"
	"testing"
)

// File logging has to be on unless the operator turns it off: a restart wipes
// stdout, and the log is the only record of what a credential did before an
// upstream account was restricted.
func TestLoggingDefaultsOnWithASizeCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg, errLoad := LoadConfigOptional(path, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional: %v", errLoad)
	}
	if !cfg.LoggingToFile {
		t.Fatal("file logging should default on")
	}
	if cfg.LogsMaxTotalSizeMB != 512 {
		t.Fatalf("size cap = %d, want 512 so enabling the files cannot fill a disk", cfg.LogsMaxTotalSizeMB)
	}
}

// An explicit false must still be honoured.
func TestExplicitFalseKeepsLoggingOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := "port: 8317\nlogging-to-file: false\nlogs-max-total-size-mb: 0\n"
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg, errLoad := LoadConfigOptional(path, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional: %v", errLoad)
	}
	if cfg.LoggingToFile {
		t.Fatal("explicit false was overridden")
	}
	if cfg.LogsMaxTotalSizeMB != 0 {
		t.Fatalf("explicit 0 cap was overridden: %d", cfg.LogsMaxTotalSizeMB)
	}
}
