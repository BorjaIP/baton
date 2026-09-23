package ctl_test

import (
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/adapters/ctl"
)

func TestParseAcquireFlags_Defaults(t *testing.T) {
	f, err := ctl.ParseAcquireFlags([]string{"src/**"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Pattern != "src/**" {
		t.Errorf("Pattern = %q, want %q", f.Pattern, "src/**")
	}
	if f.Wait != 0 {
		t.Errorf("Wait = %v, want 0", f.Wait)
	}
	if f.TTL != 5*time.Minute {
		t.Errorf("TTL = %v, want 5m", f.TTL)
	}
	if f.SlotTTL != 30*time.Second {
		t.Errorf("SlotTTL = %v, want 30s", f.SlotTTL)
	}
	if f.Mode != ctl.ModeExclusive {
		t.Errorf("Mode = %v, want Exclusive", f.Mode)
	}
}

func TestParseAcquireFlags_DashDashTerminatorAllowsPatternStartingWithDash(t *testing.T) {
	f, err := ctl.ParseAcquireFlags([]string{"--", "-weird-pattern/**"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Pattern != "-weird-pattern/**" {
		t.Errorf("Pattern = %q, want %q", f.Pattern, "-weird-pattern/**")
	}
}

func TestParseAcquireFlags_InterspersedFlags(t *testing.T) {
	f, err := ctl.ParseAcquireFlags([]string{"--mode", "shared", "src/**", "--wait", "10s"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Pattern != "src/**" {
		t.Errorf("Pattern = %q, want src/**", f.Pattern)
	}
	if f.Mode != ctl.ModeShared {
		t.Errorf("Mode = %v, want Shared", f.Mode)
	}
	if f.Wait != 10*time.Second {
		t.Errorf("Wait = %v, want 10s", f.Wait)
	}
}

func TestParseAcquireFlags_NoPositionalIsUsageError(t *testing.T) {
	if _, err := ctl.ParseAcquireFlags([]string{}); err == nil {
		t.Fatal("expected a usage error for a missing pattern")
	}
}

func TestParseAcquireFlags_ExtraPositionalIsUsageError(t *testing.T) {
	if _, err := ctl.ParseAcquireFlags([]string{"src/**", "extra"}); err == nil {
		t.Fatal("expected a usage error for an extra positional")
	}
}

func TestParseAcquireFlags_NegativeDurationIsUsageError(t *testing.T) {
	if _, err := ctl.ParseAcquireFlags([]string{"--wait", "-5s", "src/**"}); err == nil {
		t.Fatal("expected a usage error for a negative --wait")
	}
}

func TestParseAcquireFlags_MalformedDurationIsUsageError(t *testing.T) {
	if _, err := ctl.ParseAcquireFlags([]string{"--ttl", "banana", "src/**"}); err == nil {
		t.Fatal("expected a usage error for a malformed --ttl")
	}
}

func TestParseAcquireFlags_InvalidModeIsUsageError(t *testing.T) {
	if _, err := ctl.ParseAcquireFlags([]string{"--mode", "bogus", "src/**"}); err == nil {
		t.Fatal("expected a usage error for an invalid --mode")
	}
}

func TestParseTokenFlags_RequiresToken(t *testing.T) {
	if _, err := ctl.ParseReleaseFlags([]string{}); err == nil {
		t.Fatal("expected a usage error when --token is missing")
	}
	if _, err := ctl.ParseReleaseFlags([]string{"--token", "0"}); err == nil {
		t.Fatal("expected a usage error when --token is 0")
	}
	f, err := ctl.ParseReleaseFlags([]string{"--token", "42"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Token != 42 {
		t.Errorf("Token = %d, want 42", f.Token)
	}
}

func TestParseRenewFlags_DefaultsAndRequiresToken(t *testing.T) {
	if _, err := ctl.ParseRenewFlags([]string{}); err == nil {
		t.Fatal("expected a usage error when --token is missing")
	}
	f, err := ctl.ParseRenewFlags([]string{"--token", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Token != 7 {
		t.Errorf("Token = %d, want 7", f.Token)
	}
	if f.TTL != 5*time.Minute {
		t.Errorf("TTL = %v, want 5m default", f.TTL)
	}
}

func TestParseSession_FlagOverridesEnv(t *testing.T) {
	session, ephemeral := ctl.ResolveSession("flagsession", "envsession")
	if session != "flagsession" || ephemeral {
		t.Fatalf("ResolveSession = (%q, %v), want (flagsession, false)", session, ephemeral)
	}
}

func TestParseSession_EnvUsedWhenFlagEmpty(t *testing.T) {
	session, ephemeral := ctl.ResolveSession("", "envsession")
	if session != "envsession" || ephemeral {
		t.Fatalf("ResolveSession = (%q, %v), want (envsession, false)", session, ephemeral)
	}
}

func TestParseSession_GeneratesEphemeralWhenBothEmpty(t *testing.T) {
	session, ephemeral := ctl.ResolveSession("", "")
	if session == "" || !ephemeral {
		t.Fatalf("ResolveSession = (%q, %v), want (non-empty, true)", session, ephemeral)
	}
}
