package ctl

import (
	"context"
	"fmt"
	"time"

	"github.com/BorjaIP/baton/internal/client"
	"github.com/BorjaIP/baton/internal/messages"
	"github.com/BorjaIP/baton/internal/proto"
)

const envSession = "BATON_SESSION"

// Run parses and executes one ctl invocation. It never panics: every
// failure path returns a documented exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, messages.CtlUsage)
		return ExitUsage
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(env.Stdout, messages.CtlUsage)
		return ExitOK
	case "status":
		return runStatus(ctx, args[1:], env)
	case "lock":
		return runLock(ctx, args[1:], env)
	default:
		fmt.Fprintln(env.Stderr, messages.CtlUsage)
		return ExitUsage
	}
}

func runLock(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, messages.CtlUsage)
		return ExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(env.Stdout, messages.CtlUsage)
		return ExitOK
	case "acquire":
		return runAcquire(ctx, args[1:], env)
	case "release":
		return runRelease(ctx, args[1:], env)
	case "renew":
		return runRenew(ctx, args[1:], env)
	default:
		fmt.Fprintln(env.Stderr, messages.CtlUsage)
		return ExitUsage
	}
}

func dial(ctx context.Context, env Env, sessionID string, ephemeral bool) (API, error) {
	return env.Dial(ctx, client.Options{
		SessionID: sessionID,
		Ephemeral: ephemeral,
		Agent:     "baton-ctl",
		Vendor:    "baton",
		Autostart: true,
	})
}

func runAcquire(ctx context.Context, args []string, env Env) int {
	for _, a := range args {
		if a == "help" || a == "-h" || a == "--help" {
			fmt.Fprintln(env.Stdout, messages.AcquireUsage)
			return ExitOK
		}
	}

	f, err := ParseAcquireFlags(args)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	root, err := ResolveRoot(ctx, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}
	pattern, err := NormalizePattern(root, f.Pattern)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	session, ephemeral := ResolveSession(f.Session, env.Getenv(envSession))

	api, err := dial(ctx, env, session, ephemeral)
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}
	defer api.Close()

	resp, err := api.LockAcquire(ctx, proto.AcquireRequest{
		Root: root, Pattern: pattern, Mode: f.Mode.String(),
		TTLMs: f.TTL.Milliseconds(), SlotTTLMs: f.SlotTTL.Milliseconds(), WaitMs: f.Wait.Milliseconds(),
	})
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}

	RenderAcquireWithHint(env.Stdout, env.Stderr, resp, f.JSON)
	if resp.Status == "queued" {
		return ExitQueued
	}
	return ExitOK
}

func runRelease(ctx context.Context, args []string, env Env) int {
	for _, a := range args {
		if a == "help" || a == "-h" || a == "--help" {
			fmt.Fprintln(env.Stdout, messages.ReleaseUsage)
			return ExitOK
		}
	}

	f, err := ParseReleaseFlags(args)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	root, err := ResolveRoot(ctx, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	session, ephemeral := ResolveSession("", env.Getenv(envSession))
	api, err := dial(ctx, env, session, ephemeral)
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}
	defer api.Close()

	resp, err := api.LockRelease(ctx, proto.ReleaseRequest{Token: f.Token, Root: root})
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}

	RenderRelease(env.Stdout, resp, f.JSON)
	return ExitOK
}

func runRenew(ctx context.Context, args []string, env Env) int {
	for _, a := range args {
		if a == "help" || a == "-h" || a == "--help" {
			fmt.Fprintln(env.Stdout, messages.RenewUsage)
			return ExitOK
		}
	}

	f, err := ParseRenewFlags(args)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	root, err := ResolveRoot(ctx, env)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	session, ephemeral := ResolveSession("", env.Getenv(envSession))
	api, err := dial(ctx, env, session, ephemeral)
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}
	defer api.Close()

	resp, err := api.LockRenew(ctx, proto.RenewRequest{Token: f.Token, TTLMs: f.TTL.Milliseconds(), Root: root})
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}

	RenderRenew(env.Stdout, resp, f.JSON, time.Now())
	return ExitOK
}

func runStatus(ctx context.Context, args []string, env Env) int {
	for _, a := range args {
		if a == "help" || a == "-h" || a == "--help" {
			fmt.Fprintln(env.Stdout, messages.StatusUsage)
			return ExitOK
		}
	}

	f, err := ParseStatusFlags(args)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return ExitUsage
	}

	statusRoot := ""
	if !f.All {
		root, err := ResolveRoot(ctx, env)
		if err != nil {
			fmt.Fprintln(env.Stderr, err)
			return ExitUsage
		}
		statusRoot = root
	}

	session, ephemeral := ResolveSession("", env.Getenv(envSession))
	api, err := dial(ctx, env, session, ephemeral)
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}
	defer api.Close()

	resp, err := api.Status(ctx, proto.StatusRequest{Root: statusRoot, SessionID: f.Session})
	if err != nil {
		RenderError(env.Stderr, err, f.JSON)
		return ExitCode(err)
	}

	RenderStatus(env.Stdout, resp, f.JSON, f.All, time.Now())
	return ExitOK
}
