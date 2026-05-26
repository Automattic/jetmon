package processcontrol

import (
	"fmt"
	"os"
	"syscall"
)

const reexecPathEnv = "JETMON_REEXEC_PATH"

// IsGracefulRestartSignal reports whether sig requests a graceful drain followed
// by self-reexec.
func IsGracefulRestartSignal(sig os.Signal) bool {
	return sig == syscall.SIGHUP
}

// ExecSelf replaces the current process with the configured re-exec target and
// original argv/env. Docker entrypoints set JETMON_REEXEC_PATH to themselves so
// SIGHUP re-runs config rendering before the Go binary starts again. Bare
// binary installs fall back to the current executable path. Callers must
// complete graceful shutdown before invoking this; syscall.Exec does not run
// deferred cleanup handlers.
func ExecSelf() error {
	target, argv, err := ReexecTarget(os.Args, os.Getenv, os.Executable)
	if err != nil {
		return err
	}
	if err := syscall.Exec(target, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", target, err)
	}
	return nil
}

// ReexecTarget resolves the executable and argv that ExecSelf will use. It is
// exported for tests and diagnostics; production callers should use ExecSelf.
func ReexecTarget(args []string, getenv func(string) string, executable func() (string, error)) (string, []string, error) {
	target := getenv(reexecPathEnv)
	if target == "" {
		exe, err := executable()
		if err != nil {
			return "", nil, fmt.Errorf("resolve executable: %w", err)
		}
		target = exe
	}
	argv := []string{target}
	if len(args) > 1 {
		argv = append(argv, args[1:]...)
	}
	return target, argv, nil
}
