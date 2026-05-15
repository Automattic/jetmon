package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const configCommandDescription = "local operator API config"

type localConfigCommandOptions struct {
	path   string
	out    io.Writer
	errOut io.Writer
}

type localConfigView struct {
	Path         string            `json:"path"`
	Exists       bool              `json:"exists"`
	Mode         string            `json:"mode,omitempty"`
	Values       map[string]string `json:"values,omitempty"`
	Effective    map[string]string `json:"effective,omitempty"`
	EnvOverrides []string          `json:"env_overrides,omitempty"`
}

type localConfigKeyInfo struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Values      string `json:"values,omitempty"`
	Default     string `json:"default,omitempty"`
	Sensitive   bool   `json:"sensitive"`
	Description string `json:"description"`
}

var localConfigKeys = []localConfigKeyInfo{
	{
		Key:         "base_url",
		Type:        "url",
		Default:     defaultAPIBaseURL,
		Description: "API base URL used by `jetmon2 api` when --base-url and JETMON_API_URL are not set.",
	},
	{
		Key:         "token",
		Type:        "string",
		Sensitive:   true,
		Description: "Bearer token stored directly in the config file. Prefer token_file when possible.",
	},
	{
		Key:         "token_file",
		Type:        "path",
		Sensitive:   true,
		Description: "File containing the Bearer token. Relative paths are resolved from the config directory.",
	},
	{
		Key:         "auth_policy",
		Type:        "enum",
		Values:      "same-origin, any-origin",
		Default:     defaultAPIAuthPolicy,
		Description: "Controls when automatic Authorization and Idempotency-Key headers are attached.",
	},
	{
		Key:         "allow_remote",
		Type:        "bool",
		Default:     "false",
		Description: "Allow API writes to non-local URLs by default. Production writes should still be deliberate.",
	},
	{
		Key:         "timeout",
		Type:        "duration",
		Default:     "10s",
		Description: "HTTP request timeout for API CLI calls, such as 10s or 2m.",
	},
	{
		Key:         "output",
		Type:        "enum",
		Values:      "json, table",
		Default:     "json",
		Description: "Default response output format for API CLI commands.",
	},
	{
		Key:         "pretty",
		Type:        "bool",
		Default:     "false",
		Description: "Pretty-print JSON responses by default.",
	},
}

func cmdLocalConfig(args []string) {
	if len(args) == 0 {
		printLocalConfigUsage(os.Stderr)
		os.Exit(1)
	}
	opts := localConfigCommandOptions{out: os.Stdout, errOut: os.Stderr}
	var err error
	switch args[0] {
	case "path":
		err = cmdLocalConfigPath(args[1:], opts)
	case "show", "list":
		err = cmdLocalConfigShow(args[1:], opts)
	case "init", "create":
		err = cmdLocalConfigInit(args[1:], opts)
	case "set":
		err = cmdLocalConfigSet(args[1:], opts)
	case "unset", "delete":
		err = cmdLocalConfigUnset(args[1:], opts)
	case "keys":
		err = cmdLocalConfigKeys(args[1:], opts)
	case "--help", "-h", "help":
		printLocalConfigUsage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown local-config subcommand %q (want: path, show, init, set, unset, keys)\n", args[0])
		printLocalConfigUsage(os.Stderr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "local-config:", err)
		os.Exit(1)
	}
}

func printLocalConfigUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: jetmon2 local-config <path|show|init|set|unset|keys> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage the local operator API config used by `jetmon2 api`.")
	fmt.Fprintln(w, "This command does not edit the deployed Monitor service config.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Default path:")
	fmt.Fprintln(w, "  ~/.config/jetmon2.conf  override with JETMON_API_CONFIG or --path")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  jetmon2 local-config init --base-url https://jetmon-v2-api.example.com --token-file jetmon2-api-token")
	fmt.Fprintln(w, "  jetmon2 local-config show")
	fmt.Fprintln(w, "  jetmon2 local-config keys")
	fmt.Fprintln(w, "  jetmon2 local-config set output table")
	fmt.Fprintln(w, "  jetmon2 local-config unset allow_remote")
}

func newConfigFlagSet(name string, opts *localConfigCommandOptions) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(opts.errOut)
	defaultPath := opts.path
	if defaultPath == "" {
		defaultPath, _ = apiCLIConfigPath()
	}
	fs.StringVar(&opts.path, "path", defaultPath, "operator API config path")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of %s:\n", fs.Name())
		printAPIFlagDefaults(fs.Output(), fs)
	}
	return fs
}

func cmdLocalConfigPath(args []string, opts localConfigCommandOptions) error {
	fs := newConfigFlagSet("local-config path", &opts)
	output := fs.String("output", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 local-config path [--path=<file>] [--output=text|json]")
	}
	if opts.path == "" {
		return errors.New("no config path is available; set HOME, XDG_CONFIG_HOME, JETMON_API_CONFIG, or --path")
	}
	switch *output {
	case "", "text":
		_, err := fmt.Fprintln(opts.out, opts.path)
		return err
	case "json":
		return writeJSONValue(opts.out, map[string]string{"path": opts.path}, true)
	default:
		return errors.New("output must be one of: text, json")
	}
}

func cmdLocalConfigShow(args []string, opts localConfigCommandOptions) error {
	fs := newConfigFlagSet("local-config show", &opts)
	output := fs.String("output", "text", "output format: text, table, or json")
	fileOnly := fs.Bool("file-only", false, "show only values stored in the config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 local-config show [--path=<file>] [--file-only] [--output=text|table|json]")
	}
	view, err := buildLocalConfigView(opts.path, *fileOnly)
	if err != nil {
		return err
	}
	switch *output {
	case "", "text":
		return writeLocalConfigText(opts.out, view, *fileOnly)
	case "json":
		return writeJSONValue(opts.out, view, true)
	case "table":
		return writeLocalConfigTable(opts.out, view, *fileOnly)
	default:
		return errors.New("output must be one of: text, table, json")
	}
}

func cmdLocalConfigInit(args []string, opts localConfigCommandOptions) error {
	fs := newConfigFlagSet("local-config init", &opts)
	baseURL := fs.String("base-url", defaultAPIBaseURL, "API base URL")
	token := fs.String("token", "", "Bearer token to store directly in the config")
	tokenFile := fs.String("token-file", "", "Bearer token file path, relative paths are resolved from the config directory")
	authPolicy := fs.String("auth-policy", defaultAPIAuthPolicy, "automatic auth policy: same-origin or any-origin")
	timeout := fs.Duration("timeout", 10*time.Second, "request timeout")
	output := fs.String("default-output", "json", "default API output format: json or table")
	pretty := fs.Bool("pretty", false, "pretty-print JSON response bodies by default")
	allowRemote := fs.Bool("allow-remote", false, "allow API writes to non-local URLs by default")
	force := fs.Bool("force", false, "overwrite an existing config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 local-config init [flags]")
	}
	if opts.path == "" {
		return errors.New("no config path is available; set HOME, XDG_CONFIG_HOME, JETMON_API_CONFIG, or --path")
	}
	if *token != "" && *tokenFile != "" {
		return errors.New("use --token or --token-file, not both")
	}
	if _, err := os.Stat(opts.path); err == nil && !*force {
		return fmt.Errorf("%s already exists; use --force to overwrite", opts.path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", opts.path, err)
	}
	cfg := apiCLIConfig{
		BaseURL:    *baseURL,
		Token:      *token,
		TokenFile:  *tokenFile,
		AuthPolicy: *authPolicy,
		Timeout:    *timeout,
		Output:     *output,
	}
	if *pretty {
		cfg.Pretty = pretty
	}
	if *allowRemote {
		cfg.AllowRemote = allowRemote
	}
	if err := validateAPICLIConfigForWrite(cfg); err != nil {
		return err
	}
	if err := writeAPICLIConfigFile(opts.path, cfg); err != nil {
		return err
	}
	_, err := fmt.Fprintf(opts.out, "created %s\n", opts.path)
	return err
}

func cmdLocalConfigSet(args []string, opts localConfigCommandOptions) error {
	fs := newConfigFlagSet("local-config set", &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: jetmon2 local-config set [--path=<file>] <key> <value>")
	}
	if opts.path == "" {
		return errors.New("no config path is available; set HOME, XDG_CONFIG_HOME, JETMON_API_CONFIG, or --path")
	}
	cfg, err := loadRawAPICLIConfig(opts.path, false)
	if err != nil {
		return err
	}
	key := canonicalAPICLIConfigKey(fs.Arg(0))
	if err := setAPICLIConfigValue(&cfg, key, fs.Arg(1)); err != nil {
		return err
	}
	if err := validateAPICLIConfigForWrite(cfg); err != nil {
		return err
	}
	if err := writeAPICLIConfigFile(opts.path, cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(opts.out, "set %s in %s\n", key, opts.path)
	return err
}

func cmdLocalConfigUnset(args []string, opts localConfigCommandOptions) error {
	fs := newConfigFlagSet("local-config unset", &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 local-config unset [--path=<file>] <key>")
	}
	if opts.path == "" {
		return errors.New("no config path is available; set HOME, XDG_CONFIG_HOME, JETMON_API_CONFIG, or --path")
	}
	cfg, err := loadRawAPICLIConfig(opts.path, true)
	if err != nil {
		return err
	}
	key := canonicalAPICLIConfigKey(fs.Arg(0))
	if err := unsetAPICLIConfigValue(&cfg, key); err != nil {
		return err
	}
	if err := validateAPICLIConfigForWrite(cfg); err != nil {
		return err
	}
	if err := writeAPICLIConfigFile(opts.path, cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(opts.out, "unset %s in %s\n", key, opts.path)
	return err
}

func cmdLocalConfigKeys(args []string, opts localConfigCommandOptions) error {
	fs := flag.NewFlagSet("local-config keys", flag.ContinueOnError)
	fs.SetOutput(opts.errOut)
	output := fs.String("output", "table", "output format: table or json")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of %s:\n", fs.Name())
		printAPIFlagDefaults(fs.Output(), fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 local-config keys [--output=table|json]")
	}
	switch *output {
	case "", "table", "text":
		return writeLocalConfigKeysTable(opts.out, localConfigKeys)
	case "json":
		return writeJSONValue(opts.out, map[string]any{"keys": localConfigKeys}, true)
	default:
		return errors.New("output must be one of: table, json")
	}
}

func buildLocalConfigView(path string, fileOnly bool) (localConfigView, error) {
	view := localConfigView{
		Path:   path,
		Values: map[string]string{},
	}
	if path == "" {
		return view, nil
	}
	if info, err := os.Stat(path); err == nil {
		view.Exists = true
		view.Mode = info.Mode().Perm().String()
	} else if !errors.Is(err, os.ErrNotExist) {
		return view, fmt.Errorf("stat %s: %w", path, err)
	}
	raw, err := loadRawAPICLIConfig(path, false)
	if err != nil {
		return view, err
	}
	view.Values = redactedAPICLIConfigValues(raw)
	if fileOnly {
		return view, nil
	}
	resolved, err := loadAPICLIConfig(path, false)
	if err != nil {
		return view, err
	}
	effective := apiCLIOptions{
		baseURL:    defaultAPIBaseURL,
		authPolicy: defaultAPIAuthPolicy,
		timeout:    10 * time.Second,
	}
	applyAPICLIConfig(&effective, resolved)
	applyAPICLIEnvDefaults(&effective)
	view.Effective = map[string]string{
		"base_url":      effective.baseURL,
		"auth_policy":   effective.authPolicy,
		"allow_remote":  strconv.FormatBool(effective.allowRemote),
		"pretty":        strconv.FormatBool(effective.pretty),
		"output":        effective.output,
		"timeout":       effective.timeout.String(),
		"token_present": strconv.FormatBool(strings.TrimSpace(effective.token) != ""),
		"token_source":  apiCLITokenSource(raw),
	}
	view.EnvOverrides = apiCLIEnvOverrides()
	return view, nil
}

func redactedAPICLIConfigValues(cfg apiCLIConfig) map[string]string {
	values := map[string]string{}
	if cfg.BaseURL != "" {
		values["base_url"] = cfg.BaseURL
	}
	if cfg.Token != "" {
		values["token"] = "[set]"
	}
	if cfg.TokenFile != "" {
		values["token_file"] = cfg.TokenFile
	}
	if cfg.AuthPolicy != "" {
		values["auth_policy"] = cfg.AuthPolicy
	}
	if cfg.AllowRemote != nil {
		values["allow_remote"] = strconv.FormatBool(*cfg.AllowRemote)
	}
	if cfg.Pretty != nil {
		values["pretty"] = strconv.FormatBool(*cfg.Pretty)
	}
	if cfg.Output != "" {
		values["output"] = cfg.Output
	}
	if cfg.Timeout > 0 {
		values["timeout"] = cfg.Timeout.String()
	}
	return values
}

func apiCLITokenSource(raw apiCLIConfig) string {
	if strings.TrimSpace(os.Getenv("JETMON_API_TOKEN")) != "" {
		return "env"
	}
	if strings.TrimSpace(raw.TokenFile) != "" {
		return "token_file"
	}
	if strings.TrimSpace(raw.Token) != "" {
		return "config"
	}
	return "unset"
}

func apiCLIEnvOverrides() []string {
	keys := []string{}
	for _, key := range []string{"JETMON_API_CONFIG", "JETMON_API_URL", "JETMON_API_TOKEN", "JETMON_API_AUTH_POLICY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func writeLocalConfigText(w io.Writer, view localConfigView, fileOnly bool) error {
	fmt.Fprintf(w, "path: %s\n", view.Path)
	fmt.Fprintf(w, "exists: %t\n", view.Exists)
	if view.Mode != "" {
		fmt.Fprintf(w, "mode: %s\n", view.Mode)
	}
	writeConfigValueBlock(w, "file", view.Values)
	if !fileOnly {
		writeConfigValueBlock(w, "effective", view.Effective)
		if len(view.EnvOverrides) > 0 {
			fmt.Fprintf(w, "env_overrides: %s\n", strings.Join(view.EnvOverrides, ", "))
		}
	}
	return nil
}

func writeConfigValueBlock(w io.Writer, label string, values map[string]string) {
	fmt.Fprintf(w, "%s:\n", label)
	if len(values) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, key := range sortedConfigKeys(values) {
		fmt.Fprintf(w, "  %s: %s\n", key, values[key])
	}
}

func writeLocalConfigTable(w io.Writer, view localConfigView, fileOnly bool) error {
	rows := map[string]string{}
	for k, v := range view.Values {
		rows["file."+k] = v
	}
	if !fileOnly {
		for k, v := range view.Effective {
			rows["effective."+k] = v
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "key\tvalue")
	fmt.Fprintf(tw, "path\t%s\n", view.Path)
	fmt.Fprintf(tw, "exists\t%t\n", view.Exists)
	if view.Mode != "" {
		fmt.Fprintf(tw, "mode\t%s\n", view.Mode)
	}
	for _, key := range sortedConfigKeys(rows) {
		fmt.Fprintf(tw, "%s\t%s\n", key, rows[key])
	}
	if len(view.EnvOverrides) > 0 {
		fmt.Fprintf(tw, "env_overrides\t%s\n", strings.Join(view.EnvOverrides, ","))
	}
	return tw.Flush()
}

func writeLocalConfigKeysTable(w io.Writer, keys []localConfigKeyInfo) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "key\ttype\tvalues\tdefault\tsensitive\tdescription")
	for _, key := range keys {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
			key.Key,
			key.Type,
			key.Values,
			key.Default,
			key.Sensitive,
			key.Description,
		)
	}
	return tw.Flush()
}

func sortedConfigKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeJSONValue(w io.Writer, value any, pretty bool) error {
	var body []byte
	var err error
	if pretty {
		body, err = json.MarshalIndent(value, "", "  ")
	} else {
		body, err = json.Marshal(value)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}

func canonicalAPICLIConfigKey(key string) string {
	return normalizeAPICLIConfigKey(key)
}

func unsetAPICLIConfigValue(cfg *apiCLIConfig, key string) error {
	switch key {
	case "url", "base_url":
		cfg.BaseURL = ""
	case "token":
		cfg.Token = ""
	case "token_file":
		cfg.TokenFile = ""
	case "auth_policy":
		cfg.AuthPolicy = ""
	case "allow_remote":
		cfg.AllowRemote = nil
	case "pretty":
		cfg.Pretty = nil
	case "output":
		cfg.Output = ""
	case "timeout":
		cfg.Timeout = 0
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return nil
}

func validateAPICLIConfigForWrite(cfg apiCLIConfig) error {
	if strings.TrimSpace(cfg.BaseURL) != "" {
		if _, err := apiRequestURL(cfg.BaseURL, "/api/v1/health"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.AuthPolicy) != "" {
		if _, err := shouldSendAPIAutoAuth(defaultAPIBaseURL, defaultAPIBaseURL+"/api/v1/health", cfg.AuthPolicy); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.Output) != "" {
		if err := validateAPIOutputFormat(cfg.Output); err != nil {
			return err
		}
	}
	if cfg.Timeout < 0 {
		return errors.New("timeout must be positive")
	}
	if strings.TrimSpace(cfg.Token) != "" && strings.TrimSpace(cfg.TokenFile) != "" {
		return errors.New("token and token_file cannot both be set")
	}
	return nil
}

func writeAPICLIConfigFile(path string, cfg apiCLIConfig) error {
	if path == "" {
		return errors.New("config path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	body := renderAPICLIConfig(cfg)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jetmon2.conf-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return os.Chmod(path, 0600)
}

func renderAPICLIConfig(cfg apiCLIConfig) string {
	lines := []string{
		"# Jetmon operator API CLI config.",
		"# This file is read by `jetmon2 api`; it is not the Monitor service config.",
	}
	add := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		lines = append(lines, fmt.Sprintf("%s = %s", key, quoteAPICLIConfigValue(value)))
	}
	add("base_url", cfg.BaseURL)
	add("token", cfg.Token)
	add("token_file", cfg.TokenFile)
	add("auth_policy", cfg.AuthPolicy)
	if cfg.AllowRemote != nil {
		add("allow_remote", strconv.FormatBool(*cfg.AllowRemote))
	}
	if cfg.Pretty != nil {
		add("pretty", strconv.FormatBool(*cfg.Pretty))
	}
	add("output", cfg.Output)
	if cfg.Timeout > 0 {
		add("timeout", cfg.Timeout.String())
	}
	return strings.Join(lines, "\n") + "\n"
}

func quoteAPICLIConfigValue(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t#\"'") {
		return strconv.Quote(value)
	}
	return value
}
