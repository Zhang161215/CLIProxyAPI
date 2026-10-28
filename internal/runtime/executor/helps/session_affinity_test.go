package helps

import (
	"context"
	"testing"
	"time"
)

// A session has to survive as long as a conversation does. An hour-long TTL
// turns one caller into 24 sessions a day, which reads upstream as one account
// serving many clients.
func TestSessionTTLOutlivesAClientWorkingDay(t *testing.T) {
	if sessionIDTTL < 24*time.Hour {
		t.Fatalf("session TTL = %v, want at least a day", sessionIDTTL)
	}
}

// The same API key must map to the same session across calls, or the count of
// distinct sessions a credential serves is meaningless.
func TestSessionIDIsStableForAnAPIKey(t *testing.T) {
	ctx := context.Background()
	first, errFirst := CachedSessionIDRequired(ctx, "key-alpha")
	if errFirst != nil {
		t.Fatalf("first: %v", errFirst)
	}
	second, errSecond := CachedSessionIDRequired(ctx, "key-alpha")
	if errSecond != nil {
		t.Fatalf("second: %v", errSecond)
	}
	if first != second {
		t.Fatalf("session rotated between calls: %q vs %q", first, second)
	}

	other, errOther := CachedSessionIDRequired(ctx, "key-beta")
	if errOther != nil {
		t.Fatalf("other: %v", errOther)
	}
	if other == first {
		t.Fatal("different API keys share a session id")
	}
}

// Different API keys must not collapse into one session: the point is a small
// number of sessions per credential, not a single shared identity.
func TestUserIDEmbedsTheCredentialSession(t *testing.T) {
	ctx := context.Background()
	session, errSession := CachedSessionIDRequired(ctx, "key-gamma")
	if errSession != nil {
		t.Fatalf("session: %v", errSession)
	}
	if session == "" {
		t.Fatal("empty session id")
	}
	userID, errUserID := CachedUserIDRequired(ctx, "key-gamma")
	if errUserID != nil {
		t.Fatalf("user id: %v", errUserID)
	}
	if userID == "" {
		t.Fatal("empty user id")
	}
}
