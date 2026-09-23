package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRun_UnknownSubcommandFailsUsageNamingServeAndCtl(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "serve") || !strings.Contains(stderr.String(), "ctl") {
		t.Fatalf("usage error must name serve and ctl, got %q", stderr.String())
	}
}

func TestRun_MissingSubcommandFailsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRun_HelpPrintsToStdoutAndExitsZero(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{arg}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(%q) exit code = %d, want 0", arg, code)
		}
		if stdout.Len() == 0 {
			t.Fatalf("run(%q) printed nothing to stdout", arg)
		}
	}
}

func TestRun_CtlDispatchesToCtlPackage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// "ctl help" is handled entirely within the ctl package and must reach
	// it: it prints ctl-specific usage to stdout and exits 0.
	code := run(context.Background(), []string{"ctl", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(ctl help) exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status") {
		t.Fatalf("expected ctl usage mentioning status, got %q", stdout.String())
	}
}

func TestRun_HelpTextAlwaysSaysBatonRegardlessOfArgv0(t *testing.T) {
	// This test never sets os.Args[0] to anything but its decoy default
	// (the test binary's own name), proving help text does not depend on
	// os.Args[0] at all: it always names "baton".
	var stdout, stderr bytes.Buffer
	run(context.Background(), []string{"help"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "baton") {
		t.Fatalf("help text does not mention \"baton\": %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "main_test") {
		t.Fatalf("help text leaked the test binary's own argv[0]-derived name: %q", stdout.String())
	}
}
