package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultAPICLIConfigName = "jetmon2.conf"

type apiCLIConfig struct {
	BaseURL     string
	Token       string
	TokenFile   string
	AuthPolicy  string
	AllowRemote *bool
	Pretty      *bool
	Output      string
	Timeout     time.Duration
}

var apiFlagSetConfigErrors = map[*flag.FlagSet]error{}

func applyAPICLIConfigDefaults(opts *apiCLIOptions) {
	path, required := apiCLIConfigPath()
	if path == "" {
		return
	}
	cfg, err := loadAPICLIConfig(path, required)
	if err != nil {
		opts.configError = err
		return
	}
	applyAPICLIConfig(opts, cfg)
}

func applyAPICLIEnvDefaults(opts *apiCLIOptions) {
	if value := strings.TrimSpace(os.Getenv("JETMON_API_URL")); value != "" {
		opts.baseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("JETMON_API_TOKEN")); value != "" {
		opts.token = value
	}
	if value := strings.TrimSpace(os.Getenv("JETMON_API_AUTH_POLICY")); value != "" {
		opts.authPolicy = value
	}
}

func apiCLIConfigPath() (string, bool) {
	if raw, ok := os.LookupEnv("JETMON_API_CONFIG"); ok {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.EqualFold(raw, "off") || strings.EqualFold(raw, "none") {
			return "", false
		}
		return raw, true
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, defaultAPICLIConfigName), false
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, ".config", defaultAPICLIConfigName), false
	}
	return "", false
}

func loadAPICLIConfig(path string, required bool) (apiCLIConfig, error) {
	return loadAPICLIConfigFile(path, required, true)
}

func loadRawAPICLIConfig(path string, required bool) (apiCLIConfig, error) {
	return loadAPICLIConfigFile(path, required, false)
}

func loadAPICLIConfigFile(path string, required bool, resolveTokenFile bool) (apiCLIConfig, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return apiCLIConfig{}, nil
		}
		return apiCLIConfig{}, fmt.Errorf("read API CLI config %s: %w", path, err)
	}
	if info.IsDir() {
		return apiCLIConfig{}, fmt.Errorf("read API CLI config %s: is a directory", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return apiCLIConfig{}, fmt.Errorf("read API CLI config %s: %w", path, err)
	}
	defer f.Close()

	cfg := apiCLIConfig{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return apiCLIConfig{}, fmt.Errorf("parse API CLI config %s:%d: expected key=value", path, lineNo)
		}
		key = normalizeAPICLIConfigKey(key)
		value, err = parseAPICLIConfigValue(strings.TrimSpace(value))
		if err != nil {
			return apiCLIConfig{}, fmt.Errorf("parse API CLI config %s:%d: %w", path, lineNo, err)
		}
		if err := setAPICLIConfigValue(&cfg, key, value); err != nil {
			return apiCLIConfig{}, fmt.Errorf("parse API CLI config %s:%d: %w", path, lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return apiCLIConfig{}, fmt.Errorf("read API CLI config %s: %w", path, err)
	}

	if strings.TrimSpace(cfg.Token) != "" || strings.TrimSpace(cfg.TokenFile) != "" {
		if info.Mode().Perm()&0077 != 0 {
			return apiCLIConfig{}, fmt.Errorf("API CLI config %s contains token material; run chmod 600 %s", path, path)
		}
	}
	if resolveTokenFile && cfg.TokenFile != "" {
		tokenPath := cfg.TokenFile
		if !filepath.IsAbs(tokenPath) {
			tokenPath = filepath.Join(filepath.Dir(path), tokenPath)
		}
		token, err := readAPICLITokenFile(tokenPath)
		if err != nil {
			return apiCLIConfig{}, err
		}
		cfg.Token = token
	}
	return cfg, nil
}

func normalizeAPICLIConfigKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.TrimPrefix(key, "jetmon_api_")
	key = strings.TrimPrefix(key, "api_")
	key = strings.ReplaceAll(key, "-", "_")
	return key
}

func parseAPICLIConfigValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, `'`) {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", err
		}
		return unquoted, nil
	}
	return value, nil
}

func setAPICLIConfigValue(cfg *apiCLIConfig, key, value string) error {
	switch key {
	case "url", "base_url":
		cfg.BaseURL = value
	case "token":
		cfg.Token = value
	case "token_file":
		cfg.TokenFile = value
	case "auth_policy":
		cfg.AuthPolicy = value
	case "allow_remote":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("allow_remote must be true or false")
		}
		cfg.AllowRemote = &parsed
	case "pretty":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("pretty must be true or false")
		}
		cfg.Pretty = &parsed
	case "output":
		cfg.Output = value
	case "timeout":
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("timeout must be a Go duration such as 10s or 2m: %w", err)
		}
		cfg.Timeout = parsed
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return nil
}

func readAPICLITokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read API token file %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read API token file %s: is a directory", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("API token file %s contains token material; run chmod 600 %s", path, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read API token file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("API token file %s is empty", path)
	}
	return token, nil
}

func applyAPICLIConfig(opts *apiCLIOptions, cfg apiCLIConfig) {
	if strings.TrimSpace(cfg.BaseURL) != "" {
		opts.baseURL = strings.TrimSpace(cfg.BaseURL)
	}
	if strings.TrimSpace(cfg.Token) != "" {
		opts.token = strings.TrimSpace(cfg.Token)
	}
	if strings.TrimSpace(cfg.AuthPolicy) != "" {
		opts.authPolicy = strings.TrimSpace(cfg.AuthPolicy)
	}
	if cfg.AllowRemote != nil {
		opts.allowRemote = *cfg.AllowRemote
	}
	if cfg.Pretty != nil {
		opts.pretty = *cfg.Pretty
	}
	if strings.TrimSpace(cfg.Output) != "" {
		opts.output = strings.TrimSpace(cfg.Output)
	}
	if cfg.Timeout > 0 {
		opts.timeout = cfg.Timeout
	}
}

func rememberAPIConfigError(fs *flag.FlagSet, err error) {
	if fs == nil || err == nil {
		return
	}
	apiFlagSetConfigErrors[fs] = err
}

func apiCLIConfigErrorForFlagSet(fs *flag.FlagSet) error {
	if fs == nil {
		return nil
	}
	return apiFlagSetConfigErrors[fs]
}
