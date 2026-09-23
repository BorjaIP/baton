//go:build unix

package client

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/BorjaIP/baton/internal/proto"
)

// buildServeCmd constructs (without starting) the command spawnDaemon uses
// to autostart the daemon (design ADR-10): exe "serve" with no other
// arguments, running from "/" so the short-lived CLI parent's own working
// directory is never held open by the detached child, environment inherited
// plus an explicit BATON_SOCK so the spawned daemon binds the exact socket
// this client is about to retry dialing, stdin from /dev/null, stdout and
// stderr appended to the 0600 log file inside the 0700 daemon directory, and
// SysProcAttr{Setsid: true} so the child starts its own session and outlives
// this process's exit. The returned closer must be called once the command
// has been started (or failed to start), to release the two files this
// function opened.
func buildServeCmd(exe string, p proto.Paths) (cmd *exec.Cmd, closer func(), err error) {
	logFile, err := os.OpenFile(p.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		_ = logFile.Close()
		return nil, nil, err
	}

	cmd = exec.Command(exe, "serve")
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), proto.EnvSocket+"="+p.Socket)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	closer = func() {
		_ = logFile.Close()
		_ = devNull.Close()
	}
	return cmd, closer, nil
}

// spawnDaemon starts a detached "baton serve" process and immediately
// releases it, so no Wait goroutine is ever created for it (keeps this
// short-lived CLI process's own goleak clean, and lets init reap the child).
func spawnDaemon(p proto.Paths) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	cmd, closer, err := buildServeCmd(exe, p)
	if err != nil {
		return err
	}
	defer closer()

	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
