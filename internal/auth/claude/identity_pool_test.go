package claude

import "testing"

func TestDevicePoolSizeDefaultsToOne(t *testing.T) {
	t.Setenv(DevicePoolSizeEnv, "")
	if got := DevicePoolSize(); got != ClaudeDevicePoolSize {
		t.Fatalf("DevicePoolSize = %d, want %d", got, ClaudeDevicePoolSize)
	}
}

func TestDevicePoolSizeClampsInvalidValues(t *testing.T) {
	for _, raw := range []string{"0", "-3", "999", "abc"} {
		t.Setenv(DevicePoolSizeEnv, raw)
		if got := DevicePoolSize(); got != ClaudeDevicePoolSize {
			t.Fatalf("DevicePoolSize(%q) = %d, want %d", raw, got, ClaudeDevicePoolSize)
		}
	}
	t.Setenv(DevicePoolSizeEnv, "4")
	if got := DevicePoolSize(); got != 4 {
		t.Fatalf("DevicePoolSize = %d, want 4", got)
	}
}

func TestSelectDeviceIDIsStablePerSession(t *testing.T) {
	pool := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	first, errFirst := SelectDeviceID(pool, "session-one")
	if errFirst != nil {
		t.Fatalf("SelectDeviceID: %v", errFirst)
	}
	again, _ := SelectDeviceID(pool, "session-one")
	if first != again {
		t.Fatal("the same session must keep the same device ID")
	}
	// Different sessions should not all land on the same slot across a pool
	// this small, but stability matters more than spread; assert only that the
	// chosen value is a member of the pool.
	found := false
	for _, candidate := range pool {
		if candidate == first {
			found = true
		}
	}
	if !found {
		t.Fatalf("device %q is not a member of the pool", first)
	}
}

func TestSelectDeviceIDSingleEntryIgnoresSession(t *testing.T) {
	pool := []string{"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}
	for _, session := range []string{"a", "b", "c"} {
		got, errSelect := SelectDeviceID(pool, session)
		if errSelect != nil {
			t.Fatalf("SelectDeviceID: %v", errSelect)
		}
		if got != pool[0] {
			t.Fatalf("single-entry pool returned %q", got)
		}
	}
}
