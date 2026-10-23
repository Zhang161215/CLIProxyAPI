package versionguard

import (
	"testing"
)

// The shipped identity must be internally consistent; a drifted one must be
// refused. The drifted case is constructed explicitly so this keeps testing the
// invariant after the baseline was aligned.
func TestCheckRefusesDriftedIdentity(t *testing.T) {
	if errDrift := Assert(Canonical()); errDrift != nil {
		t.Fatalf("the shipped identity must not drift: %v", errDrift)
	}

	drifted := Canonical()
	drifted.TLSHelloFrom = "2.1.220"
	if errCheck := Check(drifted, Options{}); errCheck == nil {
		t.Fatal("Check must return an error when the identity drifted and the operator did not opt out")
	}
}

func TestCheckAllowsDriftWithOptIn(t *testing.T) {
	profile := Canonical()
	profile.CCHFrom = "0.0.1"
	if errCheck := Check(profile, Options{AllowDrift: true}); errCheck != nil {
		t.Fatalf("Check must not fail when AllowDrift is set, got %v", errCheck)
	}
}

func TestCheckAcceptsConsistentIdentity(t *testing.T) {
	profile := Canonical()
	profile.TLSHelloFrom = profile.Version
	profile.CCHFrom = profile.Version
	if errCheck := Check(profile, Options{}); errCheck != nil {
		t.Fatalf("a fully aligned identity must pass, got %v", errCheck)
	}
}

func TestSeedForKnownVersion(t *testing.T) {
	seed, ok := SeedFor("2.1.177")
	if !ok {
		t.Fatal("2.1.177 must have a verified seed")
	}
	if seed == 0 {
		t.Fatal("seed must not be zero")
	}
}

func TestSeedForUnknownVersionIsNotGuessed(t *testing.T) {
	if _, ok := SeedFor("9.9.9"); ok {
		t.Fatal("an unverified version must not report a seed; guessing produces a stable wrong digest")
	}
	if _, ok := SeedFor(""); ok {
		t.Fatal("an empty version must not report a seed")
	}
}

func TestOptionsFromEnvUnset(t *testing.T) {
	t.Setenv(AllowDriftEnv, "")
	if opts := OptionsFromEnv(); opts.AllowDrift {
		t.Fatal("unset env must not allow drift")
	}
}

func TestOptionsFromEnvParses(t *testing.T) {
	t.Setenv(AllowDriftEnv, "true")
	if opts := OptionsFromEnv(); !opts.AllowDrift {
		t.Fatal("true must allow drift")
	}
	t.Setenv(AllowDriftEnv, "nonsense")
	if opts := OptionsFromEnv(); opts.AllowDrift {
		t.Fatal("an unparsable value must fall back to refusing drift")
	}
}
