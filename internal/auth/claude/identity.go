package claude

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	ClaudeDeviceIDsMetadataKey = "claude_device_ids"
	// ClaudeDevicePoolSize is the default pool size. One device ID is what the
	// upstream client itself uses; larger pools only make sense when a
	// deployment multiplexes independent conversations onto one credential.
	ClaudeDevicePoolSize = 1
	// DevicePoolSizeEnv raises the pool size for new credentials. Existing
	// credentials keep whatever pool they already have: resizing would rotate
	// a device ID the upstream has already seen.
	DevicePoolSizeEnv       = "CPA_ANTIDETECT_DEVICE_POOL"
	maxClaudeDevicePoolSize = 8
	claudeDeviceIDByteSize  = 32
)

// DevicePoolSize reports how many device IDs a newly created credential gets.
func DevicePoolSize() int {
	raw := strings.TrimSpace(os.Getenv(DevicePoolSizeEnv))
	if raw == "" {
		return ClaudeDevicePoolSize
	}
	size, errParse := strconv.Atoi(raw)
	if errParse != nil || size < 1 || size > maxClaudeDevicePoolSize {
		return ClaudeDevicePoolSize
	}
	return size
}

// deviceIndexForSession maps a session to a stable slot in the pool, so one
// conversation keeps one device ID while separate conversations differ.
func deviceIndexForSession(sessionID string, size int) int {
	if size <= 1 {
		return 0
	}
	sum := sha256.Sum256([]byte("cpa-device-session|" + sessionID))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(size))
}

// claudeDevicePoolMu guards every concurrent access to a Claude credential's
// Auth.Metadata map, not just the device pool. A single Auth is shared by all
// in-flight requests using that credential, and Go maps are not safe for
// concurrent read/write, so the account-profile and refresh paths have to take
// the same lock as the pool paths. Reaching into Auth.Metadata directly from a
// request path is a data race even when the keys differ.
var claudeDevicePoolMu sync.Mutex

// GenerateDeviceIDPool creates the fixed-size device pool stored with a Claude credential.
func GenerateDeviceIDPool() ([]string, error) {
	size := DevicePoolSize()
	deviceIDs := make([]string, 0, size)
	seen := make(map[string]struct{}, size)
	for len(deviceIDs) < size {
		deviceID, errDeviceID := generateDeviceID()
		if errDeviceID != nil {
			return nil, errDeviceID
		}
		if _, exists := seen[deviceID]; exists {
			continue
		}
		seen[deviceID] = struct{}{}
		deviceIDs = append(deviceIDs, deviceID)
	}
	return deviceIDs, nil
}

func generateDeviceID() (string, error) {
	data := make([]byte, claudeDeviceIDByteSize)
	if _, errRead := rand.Read(data); errRead != nil {
		return "", fmt.Errorf("generate Claude device ID: %w", errRead)
	}
	return hex.EncodeToString(data), nil
}

// NormalizeDeviceIDPool returns the first valid device ID in canonical form.
func NormalizeDeviceIDPool(raw any) []string {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for _, value := range typed {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
	default:
		return nil
	}

	// Normalization keeps a pool as large as the operator configured, up to the
	// hard cap. Truncating to the default here silently undid DevicePoolSize for
	// every credential whose pool was read back from metadata.
	limit := min(len(values), maxClaudeDevicePoolSize)
	deviceIDs := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, value := range values {
		deviceID := strings.ToLower(strings.TrimSpace(value))
		if !ValidDeviceID(deviceID) {
			continue
		}
		if _, exists := seen[deviceID]; exists {
			continue
		}
		seen[deviceID] = struct{}{}
		deviceIDs = append(deviceIDs, deviceID)
		if len(deviceIDs) == limit {
			break
		}
	}
	return deviceIDs
}

// HasCanonicalDeviceIDPool reports whether raw already stores a normalized,
// valid pool. Any size within the cap qualifies: the canonical form is about
// shape, not about a fixed length.
func HasCanonicalDeviceIDPool(raw any) bool {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for _, value := range typed {
			text, ok := value.(string)
			if !ok {
				return false
			}
			values = append(values, text)
		}
	default:
		return false
	}
	normalized := NormalizeDeviceIDPool(values)
	if len(normalized) == 0 || len(normalized) != len(values) {
		return false
	}
	for index := range normalized {
		if normalized[index] != values[index] {
			return false
		}
	}
	// Shape alone is not enough: a legacy pool of five is well-formed but not
	// what the client presents, so it still needs migration.
	return len(values) == DevicePoolSize()
}

// EnsureDeviceIDPool repairs or creates the single-device pool in credential metadata.
func EnsureDeviceIDPool(metadata map[string]any) ([]string, bool, error) {
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	return ensureDeviceIDPoolLocked(metadata)
}

// EnsureDeviceIDPoolFor lazily initializes the metadata map and then ensures the
// pool, both under the device pool lock.
//
// A single *Auth is shared by every concurrent request that selects the same
// credential, so initializing the map field outside this lock races with the
// writes below and can abort the process with "concurrent map writes". Callers
// holding a shared credential must reach the pool through this package rather
// than touching the map directly.
func EnsureDeviceIDPoolFor(metadata *map[string]any) ([]string, bool, error) {
	if metadata == nil {
		return nil, false, fmt.Errorf("ensure Claude device pool: metadata pointer is nil")
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	return ensureDeviceIDPoolLocked(*metadata)
}

// ReadDeviceIDPool returns the stored pool value, initializing the map when
// needed, under the device pool lock. Slice values are copied so a caller can
// never mutate the stored credential identity after the lock is released.
func ReadDeviceIDPool(metadata *map[string]any) any {
	if metadata == nil {
		return nil
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
		return nil
	}
	switch stored := (*metadata)[ClaudeDeviceIDsMetadataKey].(type) {
	case []string:
		return append([]string(nil), stored...)
	case []any:
		return append([]any(nil), stored...)
	default:
		return stored
	}
}

// StoreDeviceIDPool writes a defensive copy of deviceIDs under the device pool lock.
func StoreDeviceIDPool(metadata *map[string]any, deviceIDs []string) {
	if metadata == nil {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	(*metadata)[ClaudeDeviceIDsMetadataKey] = append([]string(nil), deviceIDs...)
}

// ReadMetadataString reads a string-valued metadata entry under the metadata
// lock, so it cannot observe a map being concurrently written by another path.
func ReadMetadataString(metadata *map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return ""
	}
	value, _ := (*metadata)[key].(string)
	return value
}

// StoreMetadataString writes a string-valued metadata entry under the metadata
// lock, initializing the map when needed. Empty values are skipped so callers can
// forward optional fields without erasing a previously resolved value.
func StoreMetadataString(metadata *map[string]any, key, value string) {
	if metadata == nil || strings.TrimSpace(value) == "" {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	(*metadata)[key] = value
}

// StoreMetadataValue writes an arbitrary metadata entry under the metadata lock,
// initializing the map when needed.
func StoreMetadataValue(metadata *map[string]any, key string, value any) {
	if metadata == nil {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	(*metadata)[key] = value
}

// EnsureMetadataMap initializes the metadata map under the metadata lock.
func EnsureMetadataMap(metadata *map[string]any) {
	if metadata == nil {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
}

// ensureDeviceIDPoolLocked requires claudeDevicePoolMu to be held.
func ensureDeviceIDPoolLocked(metadata map[string]any) ([]string, bool, error) {
	if metadata == nil {
		return nil, false, fmt.Errorf("ensure Claude device pool: metadata is nil")
	}
	rawDeviceIDs := metadata[ClaudeDeviceIDsMetadataKey]
	deviceIDs := NormalizeDeviceIDPool(rawDeviceIDs)
	changed := !HasCanonicalDeviceIDPool(rawDeviceIDs)

	// The pool size is the configured size, defaulting to one. With the default
	// this reproduces the upstream contract exactly: a legacy multi-slot pool is
	// collapsed to a single device, which is what the real client presents. Only
	// an explicit CPA_ANTIDETECT_DEVICE_POOL raises it, and then the pool is
	// adjusted to that size.
	target := DevicePoolSize()
	if len(deviceIDs) > target {
		deviceIDs = deviceIDs[:target]
		changed = true
	}
	seen := make(map[string]struct{}, target)
	for _, deviceID := range deviceIDs {
		seen[deviceID] = struct{}{}
	}
	for len(deviceIDs) < target {
		deviceID, errDeviceID := generateDeviceID()
		if errDeviceID != nil {
			return nil, false, errDeviceID
		}
		if _, exists := seen[deviceID]; exists {
			continue
		}
		seen[deviceID] = struct{}{}
		deviceIDs = append(deviceIDs, deviceID)
	}

	if changed {
		metadata[ClaudeDeviceIDsMetadataKey] = append([]string(nil), deviceIDs...)
	}
	return append([]string(nil), deviceIDs...), changed, nil
}

// SelectDeviceID returns the device ID for a conversation session.
//
// A single-entry pool always returns that entry, which is the upstream client's
// own behaviour. Multi-entry pools map the session to a stable slot: rotating
// per request would tell the upstream that one machine hosts unrelated
// conversations.
func SelectDeviceID(deviceIDs []string, sessionID string) (string, error) {
	deviceIDs = NormalizeDeviceIDPool(deviceIDs)
	if len(deviceIDs) == 0 {
		return "", fmt.Errorf("select Claude device ID: device pool is empty")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("select Claude device ID: session ID is empty")
	}
	return deviceIDs[deviceIndexForSession(sessionID, len(deviceIDs))], nil
}

// ValidDeviceID reports whether a value matches Claude Code's lowercase 64-hex device format.
func ValidDeviceID(value string) bool {
	if len(value) != claudeDeviceIDByteSize*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, errDecode := hex.DecodeString(value)
	return errDecode == nil && len(decoded) == claudeDeviceIDByteSize
}

// ReadMetadataBool reads a boolean metadata entry under the metadata lock.
//
// The lock has to cover readers as well as writers: a credential serving two
// requests at once has one goroutine storing the account UUID while another
// inspects setup-token flags, and an unguarded read there is a concurrent map
// access, which is a fatal runtime error rather than a lost update.
func ReadMetadataBool(metadata *map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return false
	}
	value, _ := (*metadata)[key].(bool)
	return value
}

// ReadMetadataValue reads an arbitrary metadata entry under the metadata lock.
func ReadMetadataValue(metadata *map[string]any, key string) (any, bool) {
	if metadata == nil {
		return nil, false
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return nil, false
	}
	value, ok := (*metadata)[key]
	return value, ok
}
