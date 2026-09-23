package ctl

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/BorjaIP/baton/internal/messages"
	"github.com/oklog/ulid/v2"
)

// Mode mirrors the wire-level lock.acquire mode string ("exclusive" or
// "shared"). ctl never imports internal/core/lock directly (design §4
// layering); it only ever carries this string through to proto.AcquireRequest.
type Mode string

const (
	ModeExclusive Mode = "exclusive"
	ModeShared    Mode = "shared"
)

// String implements fmt.Stringer.
func (m Mode) String() string { return string(m) }

// AcquireFlags holds the parsed arguments for "ctl lock acquire".
type AcquireFlags struct {
	Pattern string
	Mode    Mode
	Wait    time.Duration
	TTL     time.Duration
	SlotTTL time.Duration
	Session string
	JSON    bool
}

// ReleaseFlags holds the parsed arguments for "ctl lock release".
type ReleaseFlags struct {
	Token uint64
	JSON  bool
}

// RenewFlags holds the parsed arguments for "ctl lock renew".
type RenewFlags struct {
	Token uint64
	TTL   time.Duration
	JSON  bool
}

// StatusFlags holds the parsed arguments for "ctl status".
type StatusFlags struct {
	Session string
	All     bool
	JSON    bool
}

// newFlagSet builds a flag.FlagSet configured per design: errors are
// reported by the caller (ContinueOnError), output goes to discard because
// ctl formats its own usage errors.
func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

// parseInterspersed repeatedly parses fs against args so that flags and
// positionals may be interspersed, honoring a "--" terminator. It returns
// every positional argument encountered, in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		remaining := fs.Args()
		if len(remaining) == 0 {
			break
		}
		positionals = append(positionals, remaining[0])
		rest = remaining[1:]
	}
	return positionals, nil
}

// ParseAcquireFlags parses "ctl lock acquire" arguments.
func ParseAcquireFlags(args []string) (AcquireFlags, error) {
	f := AcquireFlags{TTL: 5 * time.Minute, SlotTTL: 30 * time.Second, Mode: ModeExclusive}

	fs := newFlagSet("lock acquire", io.Discard)
	modeStr := fs.String("mode", "exclusive", "exclusive or shared")
	fs.DurationVar(&f.Wait, "wait", 0, "how long to block waiting for a grant")
	fs.DurationVar(&f.TTL, "ttl", 5*time.Minute, "grant TTL once acquired")
	fs.DurationVar(&f.SlotTTL, "slot-ttl", 30*time.Second, "queued slot TTL")
	fs.StringVar(&f.Session, "session", "", "stable session id")
	fs.BoolVar(&f.JSON, "json", false, "machine-readable JSON output")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return AcquireFlags{}, err
	}
	if len(positionals) != 1 {
		return AcquireFlags{}, fmt.Errorf("ctl: lock acquire requires exactly one pattern argument\n\n%s", messages.AcquireUsage)
	}
	f.Pattern = positionals[0]

	switch *modeStr {
	case "exclusive":
		f.Mode = ModeExclusive
	case "shared":
		f.Mode = ModeShared
	default:
		return AcquireFlags{}, fmt.Errorf("ctl: --mode must be \"exclusive\" or \"shared\", got %q", *modeStr)
	}

	if f.Wait < 0 {
		return AcquireFlags{}, fmt.Errorf("ctl: --wait must be >= 0")
	}
	if f.TTL <= 0 {
		return AcquireFlags{}, fmt.Errorf("ctl: --ttl must be > 0")
	}
	if f.SlotTTL <= 0 {
		return AcquireFlags{}, fmt.Errorf("ctl: --slot-ttl must be > 0")
	}

	return f, nil
}

// ParseReleaseFlags parses "ctl lock release" arguments.
func ParseReleaseFlags(args []string) (ReleaseFlags, error) {
	var f ReleaseFlags
	fs := newFlagSet("lock release", io.Discard)
	fs.Uint64Var(&f.Token, "token", 0, "fencing token to release")
	fs.BoolVar(&f.JSON, "json", false, "machine-readable JSON output")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return ReleaseFlags{}, err
	}
	if len(positionals) != 0 {
		return ReleaseFlags{}, fmt.Errorf("ctl: lock release takes no positional arguments\n\n%s", messages.ReleaseUsage)
	}
	if f.Token == 0 {
		return ReleaseFlags{}, fmt.Errorf("ctl: --token is required and must be non-zero\n\n%s", messages.ReleaseUsage)
	}
	return f, nil
}

// ParseRenewFlags parses "ctl lock renew" arguments.
func ParseRenewFlags(args []string) (RenewFlags, error) {
	f := RenewFlags{TTL: 5 * time.Minute}
	fs := newFlagSet("lock renew", io.Discard)
	fs.Uint64Var(&f.Token, "token", 0, "fencing token to renew")
	fs.DurationVar(&f.TTL, "ttl", 5*time.Minute, "new grant TTL")
	fs.BoolVar(&f.JSON, "json", false, "machine-readable JSON output")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return RenewFlags{}, err
	}
	if len(positionals) != 0 {
		return RenewFlags{}, fmt.Errorf("ctl: lock renew takes no positional arguments\n\n%s", messages.RenewUsage)
	}
	if f.Token == 0 {
		return RenewFlags{}, fmt.Errorf("ctl: --token is required and must be non-zero\n\n%s", messages.RenewUsage)
	}
	if f.TTL <= 0 {
		return RenewFlags{}, fmt.Errorf("ctl: --ttl must be > 0")
	}
	return f, nil
}

// ParseStatusFlags parses "ctl status" arguments.
func ParseStatusFlags(args []string) (StatusFlags, error) {
	var f StatusFlags
	fs := newFlagSet("status", io.Discard)
	fs.StringVar(&f.Session, "session", "", "filter by session id")
	fs.BoolVar(&f.All, "all", false, "show every repository, not just the current one")
	fs.BoolVar(&f.JSON, "json", false, "machine-readable JSON output")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return StatusFlags{}, err
	}
	if len(positionals) != 0 {
		return StatusFlags{}, fmt.Errorf("ctl: status takes no positional arguments\n\n%s", messages.StatusUsage)
	}
	return f, nil
}

// ResolveSession implements the documented --session precedence: the flag
// value wins, then $BATON_SESSION, then a freshly generated ephemeral ULID.
func ResolveSession(flagValue, envValue string) (session string, ephemeral bool) {
	if flagValue != "" {
		return flagValue, false
	}
	if envValue != "" {
		return envValue, false
	}
	return ulid.Make().String(), true
}
