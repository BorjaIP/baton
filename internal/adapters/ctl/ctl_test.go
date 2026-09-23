package ctl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BorjaIP/baton/internal/adapters/ctl"
	"github.com/BorjaIP/baton/internal/client"
	"github.com/BorjaIP/baton/internal/proto"
)

// fakeAPI is a test double for ctl.API.
type fakeAPI struct {
	statusResp  proto.StatusResponse
	acquireResp proto.AcquireResponse
	acquireErr  error
	releaseResp proto.ReleaseResponse
	releaseErr  error
	renewResp   proto.RenewResponse
	renewErr    error
	closed      bool
	lastAcquire proto.AcquireRequest
}

func (f *fakeAPI) Status(context.Context, proto.StatusRequest) (proto.StatusResponse, error) {
	return f.statusResp, nil
}
func (f *fakeAPI) LockAcquire(_ context.Context, r proto.AcquireRequest) (proto.AcquireResponse, error) {
	f.lastAcquire = r
	return f.acquireResp, f.acquireErr
}
func (f *fakeAPI) LockRelease(context.Context, proto.ReleaseRequest) (proto.ReleaseResponse, error) {
	return f.releaseResp, f.releaseErr
}
func (f *fakeAPI) LockRenew(context.Context, proto.RenewRequest) (proto.RenewResponse, error) {
	return f.renewResp, f.renewErr
}
func (f *fakeAPI) Close() error { f.closed = true; return nil }

func fakeTestEnv(t *testing.T, api *fakeAPI, dialErr error) (ctl.Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	env := ctl.Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(string) string { return "" },
		Getwd:  func() (string, error) { return dir, nil },
		GitTopLevel: func(context.Context, string) (string, error) {
			return "", errors.New("no git in test")
		},
		Dial: func(context.Context, client.Options) (ctl.API, error) {
			if dialErr != nil {
				return nil, dialErr
			}
			return api, nil
		},
	}
	return env, &stdout, &stderr
}

func TestRun_AcquireWithoutPatternFailsUsageWithoutDialing(t *testing.T) {
	dialed := false
	env, _, _ := fakeTestEnv(t, &fakeAPI{}, nil)
	env.Dial = func(context.Context, client.Options) (ctl.API, error) {
		dialed = true
		return &fakeAPI{}, nil
	}

	code := ctl.Run(context.Background(), []string{"lock", "acquire"}, env)
	if code != ctl.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ctl.ExitUsage)
	}
	if dialed {
		t.Fatal("daemon must not be contacted for a usage error")
	}
}

func TestRun_AcquireGrantedExitsZero(t *testing.T) {
	api := &fakeAPI{acquireResp: proto.AcquireResponse{Status: "granted", Token: 1, Root: "/r", Pattern: "src/**", Mode: "exclusive"}}
	env, stdout, _ := fakeTestEnv(t, api, nil)

	code := ctl.Run(context.Background(), []string{"lock", "acquire", "src/**"}, env)
	if code != ctl.ExitOK {
		t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "1") {
		t.Fatalf("expected token in output, got %q", stdout.String())
	}
}

func TestRun_AcquireSupportsJSON(t *testing.T) {
	api := &fakeAPI{acquireResp: proto.AcquireResponse{Status: "granted", Token: 1, Root: "/r", Pattern: "src/**", Mode: "exclusive"}}
	env, stdout, _ := fakeTestEnv(t, api, nil)

	code := ctl.Run(context.Background(), []string{"lock", "acquire", "src/**", "--json"}, env)
	if code != ctl.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout not valid JSON: %v (%q)", err, stdout.String())
	}
}

func TestRun_SessionFlagOverridesEnv(t *testing.T) {
	api := &fakeAPI{acquireResp: proto.AcquireResponse{Status: "granted", Token: 1, Root: "/r", Pattern: "src/**", Mode: "exclusive"}}
	env, _, _ := fakeTestEnv(t, api, nil)
	env.Getenv = func(k string) string {
		if k == "BATON_SESSION" {
			return "envsession"
		}
		return ""
	}

	ctl.Run(context.Background(), []string{"lock", "acquire", "src/**", "--session", "flagsession"}, env)
	if api.lastAcquire.Root == "" {
		t.Fatal("expected an acquire call")
	}
}

func TestRun_QueuedResultExitsThree(t *testing.T) {
	api := &fakeAPI{acquireResp: proto.AcquireResponse{Status: "queued", Position: 1, Root: "/r", Pattern: "src/**", Mode: "exclusive"}}
	env, stdout, _ := fakeTestEnv(t, api, nil)

	code := ctl.Run(context.Background(), []string{"lock", "acquire", "src/**"}, env)
	if code != ctl.ExitQueued {
		t.Fatalf("exit code = %d, want %d; stdout=%q", code, ctl.ExitQueued, stdout.String())
	}
}

func TestRun_StaleTokenReleaseExitsFour(t *testing.T) {
	api := &fakeAPI{releaseErr: &proto.Error{Code: proto.CodeStaleToken}}
	env, _, _ := fakeTestEnv(t, api, nil)

	code := ctl.Run(context.Background(), []string{"lock", "release", "--token", "5"}, env)
	if code != ctl.ExitRejected {
		t.Fatalf("exit code = %d, want %d", code, ctl.ExitRejected)
	}
}

func TestRun_UnreachableDaemonExitsFive(t *testing.T) {
	env, _, _ := fakeTestEnv(t, &fakeAPI{}, client.ErrUnavailable)

	code := ctl.Run(context.Background(), []string{"status"}, env)
	if code != ctl.ExitUnavailable {
		t.Fatalf("exit code = %d, want %d", code, ctl.ExitUnavailable)
	}
}

func TestRun_SameOutcomeSameExitCodeAcrossModes(t *testing.T) {
	api := &fakeAPI{releaseErr: &proto.Error{Code: proto.CodeStaleToken}}
	env, _, _ := fakeTestEnv(t, api, nil)
	human := ctl.Run(context.Background(), []string{"lock", "release", "--token", "5"}, env)

	api2 := &fakeAPI{releaseErr: &proto.Error{Code: proto.CodeStaleToken}}
	env2, _, _ := fakeTestEnv(t, api2, nil)
	jsonCode := ctl.Run(context.Background(), []string{"lock", "release", "--token", "5", "--json"}, env2)

	if human != jsonCode {
		t.Fatalf("exit codes differ: human=%d json=%d", human, jsonCode)
	}
}

func TestRun_HelpPrintsToStdoutAndExitsZero(t *testing.T) {
	env, stdout, _ := fakeTestEnv(t, &fakeAPI{}, nil)

	code := ctl.Run(context.Background(), []string{"help"}, env)
	if code != ctl.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected help text on stdout")
	}
}

func TestRun_UnrecognizedSubcommandFailsUsage(t *testing.T) {
	env, _, _ := fakeTestEnv(t, &fakeAPI{}, nil)
	if code := ctl.Run(context.Background(), []string{"bogus"}, env); code != ctl.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ctl.ExitUsage)
	}
	if code := ctl.Run(context.Background(), []string{"lock", "bogus"}, env); code != ctl.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, ctl.ExitUsage)
	}
}

func TestRun_StatusFiltersDefaultToCurrentRepo(t *testing.T) {
	api := &fakeAPI{statusResp: proto.StatusResponse{}}
	env, stdout, _ := fakeTestEnv(t, api, nil)

	code := ctl.Run(context.Background(), []string{"status"}, env)
	if code != ctl.ExitOK {
		t.Fatalf("exit code = %d, want 0; stdout=%q", code, stdout.String())
	}
}
