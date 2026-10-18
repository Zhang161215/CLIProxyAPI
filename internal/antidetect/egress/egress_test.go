package egress

import (
	"encoding/json"
	"testing"
)

func TestConfigFromEnvDisabledByDefault(t *testing.T) {
	t.Setenv(ConfigEnv, "")
	cfg, errConfig := ConfigFromEnv()
	if errConfig != nil {
		t.Fatalf("ConfigFromEnv: %v", errConfig)
	}
	if cfg.Enforce {
		t.Fatal("egress enforcement must be off unless configured")
	}
}

func TestConfigFromEnvParsesPools(t *testing.T) {
	payload, _ := json.Marshal(Config{
		Enforce: true,
		Pools:   map[string]Pool{"hk-1": {ProxyURL: "socks5://h:1080", Country: "HK"}},
	})
	t.Setenv(ConfigEnv, string(payload))

	cfg, errConfig := ConfigFromEnv()
	if errConfig != nil {
		t.Fatalf("ConfigFromEnv: %v", errConfig)
	}
	if !cfg.Enforce {
		t.Fatal("enforce must be true")
	}
	if got := cfg.Pools["hk-1"].ProxyURL; got != "socks5://h:1080" {
		t.Fatalf("proxy url = %q", got)
	}
}

// A typo in the policy must not take every request down with it.
func TestConfigFromEnvIgnoresMalformedJSON(t *testing.T) {
	t.Setenv(ConfigEnv, "{not json")
	cfg, errConfig := ConfigFromEnv()
	if errConfig == nil {
		t.Fatal("malformed JSON must be reported")
	}
	if cfg.Enforce {
		t.Fatal("a malformed policy must not enable enforcement")
	}
}

func TestCheckDisabledAllowsUnbound(t *testing.T) {
	if _, errCheck := Check(Config{}, NewBinder(), "cred-1", ""); errCheck != nil {
		t.Fatalf("enforcement off must allow an unbound credential: %v", errCheck)
	}
}

func TestCheckEnforcedRefusesUnboundCredential(t *testing.T) {
	cfg := Config{Enforce: true, Pools: map[string]Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	_, errCheck := Check(cfg, NewBinder(), "cred-1", "")
	if errCheck == nil {
		t.Fatal("an unbound credential must be refused under enforcement")
	}
}

func TestCheckEnforcedRefusesUnknownPool(t *testing.T) {
	cfg := Config{Enforce: true, Pools: map[string]Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	if _, errCheck := Check(cfg, NewBinder(), "cred-1", "p2"); errCheck == nil {
		t.Fatal("an unknown pool must be refused")
	}
}

func TestCheckEnforcedAcceptsBoundCredential(t *testing.T) {
	cfg := Config{Enforce: true, Pools: map[string]Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	pool, errCheck := Check(cfg, NewBinder(), "cred-1", "p1")
	if errCheck != nil {
		t.Fatalf("Check: %v", errCheck)
	}
	if pool.ProxyURL != "socks5://h:1" {
		t.Fatalf("proxy url = %q", pool.ProxyURL)
	}
}

// Two credentials on one exit is the condition this package exists to catch.
func TestBinderRefusesSharedExit(t *testing.T) {
	binder := NewBinder()
	if errBind := binder.Bind("p1", "cred-1"); errBind != nil {
		t.Fatalf("first bind: %v", errBind)
	}
	if errBind := binder.Bind("p1", "cred-2"); errBind == nil {
		t.Fatal("a second credential must not share the exit")
	}
}

func TestBinderIsIdempotentForSamePair(t *testing.T) {
	binder := NewBinder()
	if errBind := binder.Bind("p1", "cred-1"); errBind != nil {
		t.Fatalf("first bind: %v", errBind)
	}
	if errBind := binder.Bind("p1", "cred-1"); errBind != nil {
		t.Fatalf("rebinding the same pair must be a no-op: %v", errBind)
	}
}

func TestBinderReleasesPreviousExitOnMove(t *testing.T) {
	binder := NewBinder()
	if errBind := binder.Bind("p1", "cred-1"); errBind != nil {
		t.Fatalf("bind p1: %v", errBind)
	}
	if errBind := binder.Bind("p2", "cred-1"); errBind != nil {
		t.Fatalf("bind p2: %v", errBind)
	}
	if errBind := binder.Bind("p1", "cred-2"); errBind != nil {
		t.Fatalf("the vacated exit must be available: %v", errBind)
	}
	poolID, ok := binder.PoolFor("cred-1")
	if !ok || poolID != "p2" {
		t.Fatalf("cred-1 pool = %q (ok=%v), want p2", poolID, ok)
	}
}

func TestBinderReleaseFreesExit(t *testing.T) {
	binder := NewBinder()
	if errBind := binder.Bind("p1", "cred-1"); errBind != nil {
		t.Fatalf("bind: %v", errBind)
	}
	binder.Release("cred-1")
	if errBind := binder.Bind("p1", "cred-2"); errBind != nil {
		t.Fatalf("a released exit must be reusable: %v", errBind)
	}
}
