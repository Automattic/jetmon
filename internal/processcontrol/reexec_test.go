package processcontrol

import (
	"errors"
	"reflect"
	"syscall"
	"testing"
)

func TestIsGracefulRestartSignal(t *testing.T) {
	if !IsGracefulRestartSignal(syscall.SIGHUP) {
		t.Fatal("SIGHUP should request graceful restart")
	}
	if IsGracefulRestartSignal(syscall.SIGTERM) {
		t.Fatal("SIGTERM should request shutdown without re-exec")
	}
	if IsGracefulRestartSignal(syscall.SIGINT) {
		t.Fatal("SIGINT should request shutdown without re-exec")
	}
}

func TestReexecTargetFallsBackToExecutable(t *testing.T) {
	target, argv, err := ReexecTarget(
		[]string{"jetmon2", "serve", "--flag"},
		func(string) string { return "" },
		func() (string, error) { return "/usr/local/bin/jetmon2", nil },
	)
	if err != nil {
		t.Fatalf("ReexecTarget returned error: %v", err)
	}
	if target != "/usr/local/bin/jetmon2" {
		t.Fatalf("target = %q, want executable fallback", target)
	}
	wantArgv := []string{"/usr/local/bin/jetmon2", "serve", "--flag"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", argv, wantArgv)
	}
}

func TestReexecTargetUsesEntrypointOverride(t *testing.T) {
	target, argv, err := ReexecTarget(
		[]string{"./jetmon2"},
		func(key string) string {
			if key == reexecPathEnv {
				return "/jetmon/entrypoint.sh"
			}
			return ""
		},
		func() (string, error) { return "", errors.New("should not resolve executable") },
	)
	if err != nil {
		t.Fatalf("ReexecTarget returned error: %v", err)
	}
	if target != "/jetmon/entrypoint.sh" {
		t.Fatalf("target = %q, want entrypoint override", target)
	}
	wantArgv := []string{"/jetmon/entrypoint.sh"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", argv, wantArgv)
	}
}

func TestReexecTargetHandlesEmptyArgs(t *testing.T) {
	target, argv, err := ReexecTarget(
		nil,
		func(string) string { return "" },
		func() (string, error) { return "/usr/local/bin/veriflier2", nil },
	)
	if err != nil {
		t.Fatalf("ReexecTarget returned error: %v", err)
	}
	if target != "/usr/local/bin/veriflier2" {
		t.Fatalf("target = %q, want executable fallback", target)
	}
	wantArgv := []string{"/usr/local/bin/veriflier2"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", argv, wantArgv)
	}
}
