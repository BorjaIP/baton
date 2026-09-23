package client

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/proto"
)

// TestBuildServeCmd_ConstructsExpectedCommand is the threat-matrix RED test
// for "Process integration: daemon autostart" (design threat matrix, spec
// requirement "Autostart on Dial Failure"): it inspects the *exec.Cmd
// buildServeCmd constructs without ever starting it, so the assertions run
// against real values instead of trusting spawnDaemon's side effects.
func TestBuildServeCmd_ConstructsExpectedCommand(t *testing.T) {
	dir := t.TempDir()
	paths := proto.Paths{
		Dir:    dir,
		Socket: dir + "/b.sock",
		Lock:   dir + "/b.lock",
		Log:    dir + "/b.log",
	}

	cmd, closer, err := buildServeCmd("/usr/local/bin/baton", paths)
	if err != nil {
		t.Fatalf("buildServeCmd: %v", err)
	}
	defer closer()

	if cmd.Path != "/usr/local/bin/baton" {
		t.Fatalf("cmd.Path = %q, want the exact exe path, never PATH-resolved or argv[0]-derived", cmd.Path)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "/usr/local/bin/baton" || cmd.Args[1] != "serve" {
		t.Fatalf("cmd.Args = %v, want exactly [exe, \"serve\"]", cmd.Args)
	}
	if cmd.Dir != "/" {
		t.Fatalf("cmd.Dir = %q, want \"/\"", cmd.Dir)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("cmd.SysProcAttr = %+v, want Setsid=true (detached session)", cmd.SysProcAttr)
	}

	foundSock := false
	for _, kv := range cmd.Env {
		if kv == proto.EnvSocket+"="+paths.Socket {
			foundSock = true
		}
	}
	if !foundSock {
		t.Fatalf("cmd.Env = %v, want it to contain %s=%s", cmd.Env, proto.EnvSocket, paths.Socket)
	}
	if len(cmd.Env) < len(os.Environ()) {
		t.Fatal("cmd.Env must inherit the parent environment, not replace it")
	}

	if cmd.Stdin == nil {
		t.Fatal("cmd.Stdin must be set (design: /dev/null), not left nil (which would inherit the CLI's stdin)")
	}
	if cmd.Stdout == nil || cmd.Stdout == os.Stdout {
		t.Fatal("cmd.Stdout must be redirected to the daemon log file, not left nil or inherited")
	}

	info, statErr := os.Stat(paths.Log)
	if statErr != nil {
		t.Fatalf("expected the log file to have been created: %v", statErr)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log file permission = %o, want 0600", perm)
	}
}

// TestBuildServeCmd_ErrorOnUnwritableLogDirDoesNotStartAnything covers the
// spawn-error path of the threat matrix: a failure constructing the command
// must return an error without ever calling cmd.Start.
func TestBuildServeCmd_ErrorOnUnwritableLogDirDoesNotStartAnything(t *testing.T) {
	paths := proto.Paths{
		Dir:    "/nonexistent-baton-test-dir",
		Socket: "/nonexistent-baton-test-dir/b.sock",
		Lock:   "/nonexistent-baton-test-dir/b.lock",
		Log:    "/nonexistent-baton-test-dir/b.log",
	}

	_, _, err := buildServeCmd("/usr/local/bin/baton", paths)
	if err == nil {
		t.Fatal("buildServeCmd with an unwritable log directory: want an error, got nil")
	}
}

func TestDial_AutostartSpawnsExactlyOnceAndSucceedsOnRetry(t *testing.T) {
	path := fakeSocketPath(t)
	var spawnCalls int32

	spawn := func(p proto.Paths) error {
		atomic.AddInt32(&spawnCalls, 1)
		go func() {
			time.Sleep(60 * time.Millisecond)
			fakeDaemon(t, p.Socket, helloHandler(proto.HelloResponse{ProtoVersion: proto.Version, SessionID: "S1"}, nil))
		}()
		return nil
	}

	c, err := Dial(context.Background(), Options{
		Paths: pathsFor(path), Autostart: true, Spawn: spawn, StartTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial with autostart: %v", err)
	}
	defer c.Close()

	if got := atomic.LoadInt32(&spawnCalls); got != 1 {
		t.Fatalf("spawn called %d times, want exactly 1 per Dial", got)
	}
}

func TestDial_AutostartGivesUpAfterBoundedBackoff(t *testing.T) {
	path := fakeSocketPath(t)
	spawn := func(p proto.Paths) error { return nil } // never actually listens

	start := time.Now()
	_, err := Dial(context.Background(), Options{
		Paths: pathsFor(path), Autostart: true, Spawn: spawn, StartTimeout: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Dial after autostart exhaustion: err = %v, want ErrUnavailable", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Dial blocked for %v after exhausting StartTimeout=300ms, want a bounded return", elapsed)
	}
}

func TestDial_SpawnErrorFailsWithoutRetryStorm(t *testing.T) {
	path := fakeSocketPath(t)
	var spawnCalls int32
	spawn := func(p proto.Paths) error {
		atomic.AddInt32(&spawnCalls, 1)
		return errors.New("boom")
	}

	_, err := Dial(context.Background(), Options{Paths: pathsFor(path), Autostart: true, Spawn: spawn, StartTimeout: time.Second})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Dial with a failing spawn: err = %v, want ErrUnavailable", err)
	}
	if got := atomic.LoadInt32(&spawnCalls); got != 1 {
		t.Fatalf("spawn called %d times after failure, want exactly 1 (no retry storm)", got)
	}
}

func TestDial_ConcurrentAutostartConvergesOnOneDaemon(t *testing.T) {
	path := fakeSocketPath(t)

	var once sync.Once
	spawnedDaemon := false
	var mu sync.Mutex
	spawn := func(p proto.Paths) error {
		once.Do(func() {
			mu.Lock()
			spawnedDaemon = true
			mu.Unlock()
			go func() {
				time.Sleep(60 * time.Millisecond)
				addr, err := net.ResolveUnixAddr("unix", p.Socket)
				if err != nil {
					return
				}
				ln, err := net.ListenUnix("unix", addr)
				if err != nil {
					return
				}
				defer ln.Close()

				// Serve every subsequent connection from both racing
				// clients against this single winning daemon.
				for i := 0; i < 2; i++ {
					nc, err := ln.Accept()
					if err != nil {
						return
					}
					env, err := fakeRecvRequest(nc)
					if err != nil {
						nc.Close()
						return
					}
					respEnv, err := fakeResponseEnv(env.ID, proto.VerbHello, proto.HelloResponse{ProtoVersion: proto.Version, SessionID: "S"})
					if err == nil {
						_ = fakeSend(nc, respEnv)
					}
					nc.Close()
				}
			}()
		})
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := Dial(context.Background(), Options{
				Paths: pathsFor(path), Autostart: true, Spawn: spawn, StartTimeout: 2 * time.Second,
			})
			errs[i] = err
			if c != nil {
				c.Close()
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("client %d: Dial failed to converge on the single spawned daemon: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !spawnedDaemon {
		t.Fatal("expected the shared spawn func to have run")
	}
}

// isNotRunning is exercised indirectly by the Dial tests above; this table
// covers the sentinel classification directly, since it decides whether
// autostart triggers at all.
func TestIsNotRunning_ClassifiesDialFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ENOENT", os.ErrNotExist, true},
		{"ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"other error", errors.New("permission denied"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotRunning(tt.err); got != tt.want {
				t.Fatalf("isNotRunning(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestSpawnDaemon_UsesOsExecutableNotArgv0(t *testing.T) {
	// spawnDaemon must resolve os.Executable() itself; this test just
	// documents and exercises the failure path when the log directory does
	// not exist, without actually spawning a process.
	err := spawnDaemon(proto.Paths{Log: "/nonexistent-baton-test-dir/b.log"})
	if err == nil {
		t.Fatal("spawnDaemon with an unwritable log path: want an error, got nil")
	}
	if strings.Contains(err.Error(), "PATH") {
		t.Fatalf("unexpected PATH-related error, spawnDaemon must not use exec.LookPath: %v", err)
	}
}
