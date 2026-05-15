package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalConfigInitShowSetUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jetmon2.conf")
	tokenPath := filepath.Join(dir, "jetmon2-api-token")
	if err := os.WriteFile(tokenPath, []byte("jm_TEST\n"), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	var out bytes.Buffer
	opts := localConfigCommandOptions{path: path, out: &out, errOut: ioDiscard{}}
	err := cmdLocalConfigInit([]string{
		"--base-url", "https://jetmon-v2-api.example.test",
		"--token-file", "jetmon2-api-token",
		"--default-output", "table",
		"--allow-remote",
	}, opts)
	if err != nil {
		t.Fatalf("cmdLocalConfigInit() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("config mode = %s, want 0600", got)
	}
	raw, err := loadRawAPICLIConfig(path, true)
	if err != nil {
		t.Fatalf("loadRawAPICLIConfig() error = %v", err)
	}
	if raw.BaseURL != "https://jetmon-v2-api.example.test" || raw.TokenFile != "jetmon2-api-token" {
		t.Fatalf("raw config = %+v", raw)
	}
	if raw.AllowRemote == nil || !*raw.AllowRemote {
		t.Fatalf("allow_remote = %#v, want true", raw.AllowRemote)
	}

	out.Reset()
	err = cmdLocalConfigShow([]string{"--file-only"}, opts)
	if err != nil {
		t.Fatalf("cmdLocalConfigShow() error = %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "token_file: jetmon2-api-token") {
		t.Fatalf("show output missing token_file:\n%s", rendered)
	}
	if strings.Contains(rendered, "jm_TEST") {
		t.Fatalf("show output leaked token:\n%s", rendered)
	}

	out.Reset()
	if err := cmdLocalConfigSet([]string{"pretty", "true"}, opts); err != nil {
		t.Fatalf("cmdLocalConfigSet() error = %v", err)
	}
	raw, err = loadRawAPICLIConfig(path, true)
	if err != nil {
		t.Fatalf("loadRawAPICLIConfig() after set error = %v", err)
	}
	if raw.Pretty == nil || !*raw.Pretty {
		t.Fatalf("pretty = %#v, want true", raw.Pretty)
	}

	out.Reset()
	if err := cmdLocalConfigUnset([]string{"allow_remote"}, opts); err != nil {
		t.Fatalf("cmdLocalConfigUnset() error = %v", err)
	}
	raw, err = loadRawAPICLIConfig(path, true)
	if err != nil {
		t.Fatalf("loadRawAPICLIConfig() after unset error = %v", err)
	}
	if raw.AllowRemote != nil {
		t.Fatalf("allow_remote = %#v, want nil", raw.AllowRemote)
	}
}

func TestLocalConfigPathJSON(t *testing.T) {
	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "jetmon2.conf")
	err := cmdLocalConfigPath([]string{"--output", "json"}, localConfigCommandOptions{
		path:   path,
		out:    &out,
		errOut: ioDiscard{},
	})
	if err != nil {
		t.Fatalf("cmdLocalConfigPath() error = %v", err)
	}
	if !strings.Contains(out.String(), path) {
		t.Fatalf("output = %s, want path", out.String())
	}
}

func TestLocalConfigKeysListsSupportedKeys(t *testing.T) {
	var out bytes.Buffer
	err := cmdLocalConfigKeys(nil, localConfigCommandOptions{
		out:    &out,
		errOut: ioDiscard{},
	})
	if err != nil {
		t.Fatalf("cmdLocalConfigKeys() error = %v", err)
	}
	rendered := out.String()
	for _, want := range []string{"base_url", "token_file", "auth_policy", "allow_remote", "timeout", "output", "pretty"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("keys output missing %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(rendered, "same-origin, any-origin") {
		t.Fatalf("keys output missing auth policy values:\n%s", rendered)
	}
}
