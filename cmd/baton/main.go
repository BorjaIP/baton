// Command baton is the single entrypoint for both the lock daemon ("serve")
// and its control CLI ("ctl"). It is argv[0]-agnostic: it never branches on
// os.Args[0] and always names itself "baton" in help/usage text, so it
// behaves identically when invoked through any symlink.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/BorjaIP/baton/internal/adapters/ctl"
	"github.com/BorjaIP/baton/internal/messages"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches on args[0] only — never on os.Args[0] — to "serve", "ctl",
// or help/usage.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, messages.Usage)
		return 2
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, messages.Usage)
		return 0
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "ctl":
		return ctl.Run(ctx, args[1:], ctl.DefaultEnv(stdout, stderr))
	default:
		fmt.Fprintln(stderr, messages.Usage)
		return 2
	}
}
