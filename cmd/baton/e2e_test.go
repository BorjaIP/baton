//go:build unix

// End-to-end checkpoint tests. These build the real "baton" binary once and
// drive it as separate OS processes over an isolated $BATON_SOCK, exactly as
// two terminals would. They are skipped under -short so the fast unit suite
// stays sub-second (design "Testing Strategy" table).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---- binary build (once per test process) -----------------------------

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// buildBinary builds the "baton" binary once for the whole test process and
// returns its path. It fails the calling test (not the whole suite) if the
// build itself fails.
func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "baton-e2e-bin")
		if err != nil {
			buildErr = fmt.Errorf("mkdir temp bin dir: %w", err)
			return
		}
		bin := filepath.Join(dir, "baton")
		cmd := exec.Command(goBinary(), "build", "-o", bin, ".")
		// The test file lives in cmd/baton, and `go test` runs with that
		// directory as the working directory, so "." is this package.
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build ./cmd/baton: %w\n%s", err, out)
			return
		}
		builtBin = bin
	})
	if buildErr != nil {
		t.Fatalf("build baton binary: %v", buildErr)
	}
	return builtBin
}

// goBinary returns $GOROOT/bin/go, falling back to "go" on PATH if GOROOT is
// unset in the test environment.
func goBinary() string {
	if root := runtime.GOROOT(); root != "" {
		candidate := filepath.Join(root, "bin", "go")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "go"
}

// ---- process/environment helpers ---------------------------------------

// freshSockDir creates a short, isolated temp directory for one daemon
// instance's socket/lock/log files and returns the directory and the
// resolved socket path (asserted to fit sockaddr_un's sun_path limit).
func freshSockDir(t *testing.T) (dir, sock string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s.sock")
	if len(sock) >= 104 {
		t.Fatalf("socket path %q is too long for a portable sockaddr_un (%d bytes)", sock, len(sock))
	}
	return dir, sock
}

// freshRepoDir creates a temp working directory containing a .git directory
// so ctl's repo-root resolution succeeds via the .git-ancestor fallback,
// without requiring a real git repository or the git binary.
func freshRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("create .git dir: %v", err)
	}
	return dir
}

// daemonEnv builds a minimal, isolated environment for a ctl/serve
// invocation: a fresh $HOME (so autostart's os.UserHomeDir fallback never
// touches the real home) and the given socket path. It never touches the
// caller's real ~/.baton.
func daemonEnv(home, sock string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"BATON_SOCK=" + sock,
	}
}

func withSession(env []string, session string) []string {
	out := make([]string, len(env), len(env)+1)
	copy(out, env)
	return append(out, "BATON_SESSION="+session)
}

// syncBuffer is a concurrency-safe io.Writer/String() buffer for capturing
// output from an async subprocess while a separate goroutine calls Wait.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type ctlResult struct {
	stdout string
	stderr string
	code   int
}

// runCtlOnce runs "<bin> ctl <args...>" to completion and returns its
// output and exit code (0 for a clean exit).
func runCtlOnce(t *testing.T, bin, dir string, env []string, args ...string) ctlResult {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"ctl"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run ctl %v: %v (stderr=%s)", args, err, stderr.String())
		}
	}
	return ctlResult{stdout.String(), stderr.String(), code}
}

// asyncCtl is a "<bin> ctl <args...>" invocation started in the background,
// used for a blocking acquire (--wait > 0). done is closed exactly once
// when cmd.Wait() returns, so it can be safely observed (via select, in
// both waitForExit and stillRunning, and again from t.Cleanup) any number
// of times without a second receive blocking forever.
type asyncCtl struct {
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan struct{}
	err    error
}

func startCtlAsync(t *testing.T, bin, dir string, env []string, args ...string) *asyncCtl {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"ctl"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	out := &syncBuffer{}
	errb := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("start async ctl %v: %v", args, err)
	}
	a := &asyncCtl{cmd: cmd, stdout: out, stderr: errb, done: make(chan struct{})}
	go func() {
		a.err = cmd.Wait()
		close(a.done)
	}()
	t.Cleanup(func() {
		select {
		case <-a.done:
		default:
			_ = cmd.Process.Kill()
			<-a.done
		}
	})
	return a
}

// waitForExit waits (with a bounded deadline) for an async ctl invocation
// to finish and reports its exit code.
func waitForExit(t *testing.T, a *asyncCtl, timeout time.Duration) int {
	t.Helper()
	select {
	case <-a.done:
		if a.err == nil {
			return 0
		}
		if ee, ok := a.err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("async ctl wait: %v (stderr=%s)", a.err, a.stderr.String())
	case <-time.After(timeout):
		t.Fatalf("async ctl did not finish within %s (stdout=%s stderr=%s)", timeout, a.stdout.String(), a.stderr.String())
	}
	return -1
}

// stillRunning reports whether an async ctl invocation has not exited yet.
func stillRunning(a *asyncCtl) bool {
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stopDaemon best-effort SIGTERMs the daemon behind sockDir/env (discovered
// via "ctl status --json"'s daemon.pid) and waits for its socket file to be
// removed. It never fails the test: it is meant for t.Cleanup.
func stopDaemon(t *testing.T, bin, dir string, env []string, sockDir string) {
	t.Helper()
	res := runCtlOnce(t, bin, dir, env, "status", "--json")
	if res.code != 0 {
		return
	}
	var parsed statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &parsed); err != nil || parsed.Daemon.PID == 0 {
		return
	}
	_ = syscall.Kill(parsed.Daemon.PID, syscall.SIGTERM)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(sockDir, "s.sock")); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---- JSON shapes mirroring internal/adapters/ctl's --json output --------

type acquireJSON struct {
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	Token    uint64 `json:"token"`
	Expires  string `json:"expires"`
	Position int    `json:"position"`
	Root     string `json:"root"`
	Pattern  string `json:"pattern"`
	Mode     string `json:"mode"`
	Session  string `json:"session"`
	Resumed  bool   `json:"resumed"`
}

type renewJSON struct {
	OK      bool   `json:"ok"`
	Token   uint64 `json:"token"`
	Expires string `json:"expires"`
}

type grantJSON struct {
	Token   uint64 `json:"token"`
	Session string `json:"session"`
	Root    string `json:"root"`
	Pattern string `json:"pattern"`
	Mode    string `json:"mode"`
	Expires string `json:"expires"`
}

type pendingJSON struct {
	Session     string  `json:"session"`
	Root        string  `json:"root"`
	Pattern     string  `json:"pattern"`
	Mode        string  `json:"mode"`
	Position    int     `json:"position"`
	Awaiting    bool    `json:"awaiting"`
	SlotExpires *string `json:"slot_expires"`
}

type statusJSON struct {
	OK     bool `json:"ok"`
	Daemon struct {
		PID   int `json:"pid"`
		Proto int `json:"proto"`
	} `json:"daemon"`
	Grants []grantJSON   `json:"grants"`
	Queue  []pendingJSON `json:"queue"`
}

func statusOf(t *testing.T, bin, dir string, env []string) statusJSON {
	t.Helper()
	res := runCtlOnce(t, bin, dir, env, "status", "--json")
	if res.code != 0 {
		t.Fatalf("status --json: code=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	var s statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &s); err != nil {
		t.Fatalf("status --json decode: %v (%s)", err, res.stdout)
	}
	return s
}

// ---- the two-terminal checkpoint ----------------------------------------

func TestE2E_TwoTerminalCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e checkpoint in -short mode")
	}
	bin := buildBinary(t)
	home := t.TempDir()
	repo := freshRepoDir(t)
	sockDir, sock := freshSockDir(t)
	env := daemonEnv(home, sock)
	t.Cleanup(func() { stopDaemon(t, bin, repo, env, sockDir) })

	envA := withSession(env, "A")
	envB := withSession(env, "B")

	// Step 1: A acquires; the daemon is autostarted; exit 0; token T1.
	resA := runCtlOnce(t, bin, repo, envA, "lock", "acquire", "src/auth/**", "--json")
	if resA.code != 0 {
		t.Fatalf("A acquire: code=%d stdout=%s stderr=%s", resA.code, resA.stdout, resA.stderr)
	}
	var a acquireJSON
	if err := json.Unmarshal([]byte(resA.stdout), &a); err != nil {
		t.Fatalf("A acquire JSON: %v (%s)", err, resA.stdout)
	}
	if a.Status != "granted" || a.Token == 0 {
		t.Fatalf("A acquire result = %+v, want a granted non-zero token", a)
	}
	t1 := a.Token

	// Step 2: B, same pattern, default --wait 0 -> exit 3 "queued, position 1".
	resB1 := runCtlOnce(t, bin, repo, envB, "lock", "acquire", "src/auth/**", "--json")
	if resB1.code != 3 {
		t.Fatalf("B first acquire: code=%d stdout=%s stderr=%s", resB1.code, resB1.stdout, resB1.stderr)
	}
	var b1 acquireJSON
	if err := json.Unmarshal([]byte(resB1.stdout), &b1); err != nil {
		t.Fatalf("B first acquire JSON: %v (%s)", err, resB1.stdout)
	}
	if b1.Status != "queued" || b1.Position != 1 {
		t.Fatalf("B first acquire result = %+v, want queued at position 1", b1)
	}

	// Step 3: B re-runs with --wait 10s: resumes the same slot (no
	// ErrSessionOverlap) and blocks.
	waiter := startCtlAsync(t, bin, repo, envB, "lock", "acquire", "src/auth/**", "--wait", "10s", "--json")
	waitForCondition(t, 5*time.Second, "B never appeared as an awaiting queue entry", func() bool {
		st := statusOf(t, bin, repo, env)
		for _, p := range st.Queue {
			if p.Session == "B" && p.Awaiting {
				return true
			}
		}
		return false
	})
	if !stillRunning(waiter) {
		t.Fatalf("B's blocked acquire exited early: stdout=%s stderr=%s", waiter.stdout.String(), waiter.stderr.String())
	}

	// Step 4: A releases T1, unblocking B, which exits 0 with T2 > T1.
	resRelease1 := runCtlOnce(t, bin, repo, envA, "lock", "release", "--token", fmt.Sprint(t1), "--json")
	if resRelease1.code != 0 {
		t.Fatalf("A release T1: code=%d stdout=%s stderr=%s", resRelease1.code, resRelease1.stdout, resRelease1.stderr)
	}

	if code := waitForExit(t, waiter, 5*time.Second); code != 0 {
		t.Fatalf("B resumed acquire exit=%d, want 0 (stdout=%s stderr=%s)", code, waiter.stdout.String(), waiter.stderr.String())
	}
	var b2 acquireJSON
	if err := json.Unmarshal([]byte(waiter.stdout.String()), &b2); err != nil {
		t.Fatalf("B resumed acquire JSON: %v (%s)", err, waiter.stdout.String())
	}
	if b2.Status != "granted" || b2.Token <= t1 {
		t.Fatalf("B resumed acquire result = %+v, want granted with token > %d", b2, t1)
	}
	t2 := b2.Token

	// Step 5: A releases T1 again -> exit 4 (stale token).
	resRelease2 := runCtlOnce(t, bin, repo, envA, "lock", "release", "--token", fmt.Sprint(t1), "--json")
	if resRelease2.code != 4 {
		t.Fatalf("A release T1 again: code=%d stdout=%s stderr=%s", resRelease2.code, resRelease2.stdout, resRelease2.stderr)
	}

	// Step 6: status --json shows B holding with T2 and an empty queue.
	st := statusOf(t, bin, repo, env)
	if len(st.Queue) != 0 {
		t.Fatalf("expected an empty queue after B was granted, got %+v", st.Queue)
	}
	var found bool
	for _, g := range st.Grants {
		if g.Session == "B" && g.Token == t2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected B to hold token %d, got grants=%+v", t2, st.Grants)
	}

	// Step 7: a second concurrent autostart attempt does not create a
	// second daemon (single flock holder, same daemon pid observed by
	// every concurrent caller).
	t.Run("ConcurrentAutostartCreatesOnlyOneDaemon", func(t *testing.T) {
		freshDir, freshSock := freshSockDir(t)
		freshHome := t.TempDir()
		freshEnv := daemonEnv(freshHome, freshSock)
		freshRepo := freshRepoDir(t)
		t.Cleanup(func() { stopDaemon(t, bin, freshRepo, freshEnv, freshDir) })

		const n = 4
		pids := make([]int, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				res := runCtlOnce(t, bin, freshRepo, freshEnv, "status", "--json")
				if res.code != 0 {
					errs[i] = fmt.Errorf("code=%d stderr=%s", res.code, res.stderr)
					return
				}
				var s statusJSON
				if err := json.Unmarshal([]byte(res.stdout), &s); err != nil {
					errs[i] = err
					return
				}
				pids[i] = s.Daemon.PID
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("concurrent status[%d]: %v", i, err)
			}
		}
		for i := 1; i < n; i++ {
			if pids[i] == 0 || pids[i] != pids[0] {
				t.Fatalf("expected a single daemon pid across %d concurrent autostarts, got %v", n, pids)
			}
		}
	})
}

// TestE2E_RenewExtendsExpiryKeepsToken covers the checkpoint's "renew
// extends expiry without changing the token" success criterion.
func TestE2E_RenewExtendsExpiryKeepsToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in -short mode")
	}
	bin := buildBinary(t)
	home := t.TempDir()
	repo := freshRepoDir(t)
	sockDir, sock := freshSockDir(t)
	env := daemonEnv(home, sock)
	t.Cleanup(func() { stopDaemon(t, bin, repo, env, sockDir) })
	envA := withSession(env, "A")

	resA := runCtlOnce(t, bin, repo, envA, "lock", "acquire", "renew/pattern/**", "--ttl", "30s", "--json")
	if resA.code != 0 {
		t.Fatalf("acquire: code=%d stdout=%s stderr=%s", resA.code, resA.stdout, resA.stderr)
	}
	var a acquireJSON
	if err := json.Unmarshal([]byte(resA.stdout), &a); err != nil {
		t.Fatalf("acquire JSON: %v", err)
	}

	resR := runCtlOnce(t, bin, repo, envA, "lock", "renew", "--token", fmt.Sprint(a.Token), "--ttl", "1m", "--json")
	if resR.code != 0 {
		t.Fatalf("renew: code=%d stdout=%s stderr=%s", resR.code, resR.stdout, resR.stderr)
	}
	var r renewJSON
	if err := json.Unmarshal([]byte(resR.stdout), &r); err != nil {
		t.Fatalf("renew JSON: %v", err)
	}
	if r.Token != a.Token {
		t.Fatalf("renew changed the token: got %d, want %d", r.Token, a.Token)
	}
	expA, err := time.Parse(time.RFC3339Nano, a.Expires)
	if err != nil {
		t.Fatalf("parse acquire expires: %v", err)
	}
	expR, err := time.Parse(time.RFC3339Nano, r.Expires)
	if err != nil {
		t.Fatalf("parse renew expires: %v", err)
	}
	if !expR.After(expA) {
		t.Fatalf("renew did not extend expiry: acquire=%s renew=%s", expA, expR)
	}
}

// TestE2E_KillBlockedWaiterStableSessionKeepsSlot covers G1 for a stable
// session: killing a CLI blocked in --wait returns the server-side Await
// promptly, and the slot remains (visible in status, awaiting=false until
// SlotTTL governs it).
func TestE2E_KillBlockedWaiterStableSessionKeepsSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in -short mode")
	}
	bin := buildBinary(t)
	home := t.TempDir()
	repo := freshRepoDir(t)
	sockDir, sock := freshSockDir(t)
	env := daemonEnv(home, sock)
	t.Cleanup(func() { stopDaemon(t, bin, repo, env, sockDir) })
	envA := withSession(env, "A")
	envC := withSession(env, "C")

	resA := runCtlOnce(t, bin, repo, envA, "lock", "acquire", "stable/kill/**", "--slot-ttl", "1m", "--json")
	if resA.code != 0 {
		t.Fatalf("A acquire: code=%d stdout=%s stderr=%s", resA.code, resA.stdout, resA.stderr)
	}

	waiter := startCtlAsync(t, bin, repo, envC, "lock", "acquire", "stable/kill/**", "--wait", "10s", "--slot-ttl", "1m", "--json")
	waitForCondition(t, 5*time.Second, "C never appeared as awaiting", func() bool {
		st := statusOf(t, bin, repo, env)
		for _, p := range st.Queue {
			if p.Session == "C" && p.Awaiting {
				return true
			}
		}
		return false
	})

	if err := waiter.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal blocked waiter: %v", err)
	}
	waitForExit(t, waiter, 5*time.Second)

	waitForCondition(t, 5*time.Second, "C's slot disappeared after disconnect, expected it to be kept (stable session)", func() bool {
		st := statusOf(t, bin, repo, env)
		for _, p := range st.Queue {
			if p.Session == "C" && !p.Awaiting {
				return true
			}
		}
		return false
	})
}

// TestE2E_KillBlockedWaiterEphemeralSessionDropsSlot covers G1 for an
// ephemeral (no --session, no $BATON_SESSION) invocation: killing it while
// blocked makes its slot disappear immediately.
func TestE2E_KillBlockedWaiterEphemeralSessionDropsSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in -short mode")
	}
	bin := buildBinary(t)
	home := t.TempDir()
	repo := freshRepoDir(t)
	sockDir, sock := freshSockDir(t)
	env := daemonEnv(home, sock)
	t.Cleanup(func() { stopDaemon(t, bin, repo, env, sockDir) })
	envA := withSession(env, "A")

	const pattern = "ephemeral/kill/**"
	resA := runCtlOnce(t, bin, repo, envA, "lock", "acquire", pattern, "--slot-ttl", "1m", "--json")
	if resA.code != 0 {
		t.Fatalf("A acquire: code=%d stdout=%s stderr=%s", resA.code, resA.stdout, resA.stderr)
	}

	// No --session and no $BATON_SESSION: ctl generates a fresh ephemeral
	// session for this invocation.
	waiter := startCtlAsync(t, bin, repo, env, "lock", "acquire", pattern, "--wait", "10s", "--slot-ttl", "1m", "--json")
	waitForCondition(t, 5*time.Second, "ephemeral waiter never appeared in the queue", func() bool {
		st := statusOf(t, bin, repo, env)
		for _, p := range st.Queue {
			if p.Pattern == pattern {
				return true
			}
		}
		return false
	})

	if err := waiter.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal blocked waiter: %v", err)
	}
	waitForExit(t, waiter, 5*time.Second)

	waitForCondition(t, 5*time.Second, "ephemeral waiter's slot was not dropped immediately on disconnect (G1)", func() bool {
		st := statusOf(t, bin, repo, env)
		for _, p := range st.Queue {
			if p.Pattern == pattern {
				return false
			}
		}
		return true
	})
}

// TestE2E_BtSymlinkBehavesIdentically covers the argv[0]-agnostic
// entrypoint: a "bt" symlink to the same binary must behave identically.
func TestE2E_BtSymlinkBehavesIdentically(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in -short mode")
	}
	bin := buildBinary(t)
	linkDir := t.TempDir()
	btPath := filepath.Join(linkDir, "bt")
	if err := os.Symlink(bin, btPath); err != nil {
		t.Fatalf("create bt symlink: %v", err)
	}

	home := t.TempDir()
	repo := freshRepoDir(t)
	sockDir, sock := freshSockDir(t)
	env := daemonEnv(home, sock)
	t.Cleanup(func() { stopDaemon(t, bin, repo, env, sockDir) })

	// Establish the daemon once via the real binary name.
	resBaton := runCtlOnce(t, bin, repo, env, "status", "--json")
	if resBaton.code != 0 {
		t.Fatalf("baton ctl status --json: code=%d stderr=%s", resBaton.code, resBaton.stderr)
	}

	resBt := runCtlOnce(t, btPath, repo, env, "status", "--json")
	if resBt.code != resBaton.code {
		t.Fatalf("bt ctl status --json exit=%d, want %d", resBt.code, resBaton.code)
	}
	var stBaton, stBt statusJSON
	if err := json.Unmarshal([]byte(resBaton.stdout), &stBaton); err != nil {
		t.Fatalf("decode baton status: %v", err)
	}
	if err := json.Unmarshal([]byte(resBt.stdout), &stBt); err != nil {
		t.Fatalf("decode bt status: %v", err)
	}
	if stBt.OK != stBaton.OK || stBt.Daemon.Proto != stBaton.Daemon.Proto || stBt.Daemon.PID != stBaton.Daemon.PID {
		t.Fatalf("bt ctl status --json = %+v, want the same daemon identity as baton: %+v", stBt, stBaton)
	}

	cmd := exec.Command(btPath, "--help")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bt --help: %v (%s)", err, out)
	}
	if !bytesContains(out, "baton") {
		t.Fatalf("bt --help must print the fixed program name \"baton\", got %q", out)
	}
}

func bytesContains(b []byte, s string) bool {
	return bytes.Contains(b, []byte(s))
}
