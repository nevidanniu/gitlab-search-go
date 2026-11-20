package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	FileName           = ".gitlab-search.yaml"
	DefaultDomain      = "gitlab.com"
	DefaultConcurrency = 25
	DefaultRetryCount  = 5
	DefaultRateLimit   = 0.0
	DefaultLogLevel    = "info"
	DefaultArchive     = "all"
	DefaultFormat      = "plain"
	EnvToken           = "GITLAB_SEARCH_TOKEN"
	EnvDomain          = "GITLAB_SEARCH_DOMAIN"
	EnvConcurrency     = "GITLAB_SEARCH_CONCURRENCY"
	EnvRateLimit       = "GITLAB_SEARCH_RATE_LIMIT"
	EnvRetryCount      = "GITLAB_SEARCH_RETRY_COUNT"
	EnvIgnoreSSL       = "GITLAB_SEARCH_IGNORE_SSL"
	EnvCAFile          = "GITLAB_SEARCH_CA_FILE"
	EnvLogLevel        = "GITLAB_SEARCH_LOG_LEVEL"
	EnvLegacyDebug     = "DEBUG"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Token       string
	Domain      string
	Protocol    string
	IgnoreSSL   bool
	CAFile      string
	Concurrency int
	RateLimit   float64
	RetryCount  int
	LogLevel    slog.Level
	ConfigPath  string // empty when running on env/defaults only

	// Per-search defaults, overridable by CLI flags.
	Groups    string
	Filename  string
	Extension string
	Path      string
	Archive   string
	Format    string
	Output    string
	Quiet     bool
}

type fileFormat struct {
	Domain      *string  `yaml:"domain"`
	Token       *string  `yaml:"token"`
	IgnoreSSL   *bool    `yaml:"ignore_ssl"`
	CAFile      *string  `yaml:"ca_file"`
	Concurrency *int     `yaml:"concurrency"`
	RateLimit   *float64 `yaml:"rate_limit"`
	RetryCount  *int     `yaml:"retry_count"`
	LogLevel    *string  `yaml:"log_level"`
	Groups      *string  `yaml:"groups"`
	Filename    *string  `yaml:"filename"`
	Extension   *string  `yaml:"extension"`
	Path        *string  `yaml:"path"`
	Archive     *string  `yaml:"archive"`
	Format      *string  `yaml:"format"`
	Output      *string  `yaml:"output"`
	Quiet       *bool    `yaml:"quiet"`
}

// Override carries CLI flag values; non-empty (or set) entries win over environment and file.
type Override struct {
	Token       string
	Domain      string
	CAFile      string
	Concurrency *int
	RetryCount  *int
	RateLimit   *float64
	IgnoreSSL   *bool
}

// Load resolves configuration from explicit path or file discovery, env overrides, then flag overrides.
func Load(explicitPath string, ov *Override) (*Config, error) {
	cfg := &Config{
		Protocol:    "https",
		Domain:      DefaultDomain,
		Concurrency: DefaultConcurrency,
		RetryCount:  DefaultRetryCount,
		RateLimit:   DefaultRateLimit,
		LogLevel:    slog.LevelInfo,
	}

	path, err := findPath(explicitPath)
	if err != nil {
		return nil, err
	}
	if path != "" {
		cfg.ConfigPath = path
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not read configuration file %s: %w", path, err)
		}
		var f fileFormat
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("could not parse configuration file %s: %w", path, err)
		}
		if f.Domain != nil {
			cfg.Domain = *f.Domain
		}
		if f.Token != nil {
			cfg.Token = *f.Token
		}
		if f.IgnoreSSL != nil {
			cfg.IgnoreSSL = *f.IgnoreSSL
		}
		if f.CAFile != nil {
			cfg.CAFile = *f.CAFile
		}
		if f.Concurrency != nil {
			cfg.Concurrency = *f.Concurrency
		}
		if f.RateLimit != nil {
			cfg.RateLimit = *f.RateLimit
		}
		if f.RetryCount != nil {
			cfg.RetryCount = *f.RetryCount
		}
		if f.LogLevel != nil {
			if err := cfg.LogLevel.UnmarshalText([]byte(*f.LogLevel)); err != nil {
				return nil, fmt.Errorf("invalid log_level %q in %s", *f.LogLevel, path)
			}
		}
		if f.Groups != nil {
			cfg.Groups = *f.Groups
		}
		if f.Filename != nil {
			cfg.Filename = *f.Filename
		}
		if f.Extension != nil {
			cfg.Extension = *f.Extension
		}
		if f.Path != nil {
			cfg.Path = *f.Path
		}
		if f.Archive != nil {
			cfg.Archive = *f.Archive
		}
		if f.Format != nil {
			cfg.Format = *f.Format
		}
		if f.Output != nil {
			cfg.Output = *f.Output
		}
		if f.Quiet != nil {
			cfg.Quiet = *f.Quiet
		}
	}

	applyEnv(cfg)
	applyOverride(cfg, ov)

	if strings.TrimSpace(cfg.Token) == "" {
		if path == "" {
			return nil, fmt.Errorf("could not find %s configuration file anywhere and no %s environment variable set, run \"setup --token <personal-access-token>\" first", FileName, EnvToken)
		}
		return nil, fmt.Errorf("a personal access token is required but the token key in %s is empty, set it there, run \"setup --token <personal-access-token>\", or export %s", path, EnvToken)
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.RetryCount < 0 {
		cfg.RetryCount = DefaultRetryCount
	}

	proto, host, err := ParseDomain(cfg.Domain)
	if err != nil {
		return nil, err
	}
	cfg.Protocol, cfg.Domain = proto, host
	return cfg, nil
}

func findPath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("configuration file %s not found", explicit)
		}
		return explicit, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", nil
}

func applyEnv(cfg *Config) {
	if v, ok := os.LookupEnv(EnvToken); ok {
		cfg.Token = v
	}
	if v, ok := os.LookupEnv(EnvDomain); ok {
		cfg.Domain = v
	}
	if v, ok := os.LookupEnv(EnvConcurrency); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Concurrency = n
		}
	}
	if v, ok := os.LookupEnv(EnvRateLimit); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			cfg.RateLimit = f
		}
	}
	if v, ok := os.LookupEnv(EnvRetryCount); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.RetryCount = n
		}
	}
	if v, ok := os.LookupEnv(EnvIgnoreSSL); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.IgnoreSSL = b
		}
	}
	if v, ok := os.LookupEnv(EnvCAFile); ok {
		cfg.CAFile = v
	}
	if v, ok := os.LookupEnv(EnvLogLevel); ok {
		var lvl slog.Level
		if lvl.UnmarshalText([]byte(v)) == nil {
			cfg.LogLevel = lvl
		}
	}
}

func applyOverride(cfg *Config, ov *Override) {
	if ov == nil {
		return
	}
	if strings.TrimSpace(ov.Token) != "" {
		cfg.Token = ov.Token
	}
	if strings.TrimSpace(ov.Domain) != "" {
		cfg.Domain = ov.Domain
	}
	if ov.CAFile != "" {
		cfg.CAFile = ov.CAFile
	}
	if ov.IgnoreSSL != nil {
		cfg.IgnoreSSL = *ov.IgnoreSSL
	}
	if ov.Concurrency != nil && *ov.Concurrency > 0 {
		cfg.Concurrency = *ov.Concurrency
	}
	if ov.RetryCount != nil && *ov.RetryCount >= 0 {
		cfg.RetryCount = *ov.RetryCount
	}
	if ov.RateLimit != nil && *ov.RateLimit >= 0 {
		cfg.RateLimit = *ov.RateLimit
	}
}

// ParseDomain accepts a bare host (https) or a root URL with http(s):// scheme.
func ParseDomain(domain string) (protocol, host string, err error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		d = DefaultDomain
	}
	d = strings.TrimSuffix(d, "/")
	parts := strings.Split(d, "://")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", fmt.Errorf("configured API domain does not look like a valid domain or root GitLab API URI, please double check your configuration of: %s", domain)
		}
		return "https", parts[0], nil
	case 2:
		if parts[1] == "" || (parts[0] != "http" && parts[0] != "https") {
			return "", "", fmt.Errorf("configured API domain does not look like a valid domain or root GitLab API URI, please double check your configuration of: %s", domain)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("configured API domain does not look like a valid domain or root GitLab API URI, please double check your configuration of: %s", domain)
	}
}

// WriteOptions carries setup values; unless Full is set, keys equal to defaults are omitted.
type WriteOptions struct {
	Dir         string
	File        string // full path, overrides Dir/FileName when set
	Token       string
	Domain      string
	IgnoreSSL   bool
	CAFile      string
	Concurrency int
	RateLimit   float64
	RetryCount  int
	LogLevel    string
	Groups      string
	Filename    string
	Extension   string
	Path        string
	Archive     string
	Format      string
	Output      string
	Quiet       bool
	Full        bool
}

// Write creates the configuration YAML at File, or at <Dir>/FileName when File is empty.
func Write(o WriteOptions) (string, error) {
	var raw []byte
	var err error
	if o.Full {
		raw, err = yaml.Marshal(struct {
			Domain      string  `yaml:"domain"`
			Token       string  `yaml:"token"`
			IgnoreSSL   bool    `yaml:"ignore_ssl"`
			CAFile      string  `yaml:"ca_file"`
			Concurrency int     `yaml:"concurrency"`
			RateLimit   float64 `yaml:"rate_limit"`
			RetryCount  int     `yaml:"retry_count"`
			LogLevel    string  `yaml:"log_level"`
			Groups      string  `yaml:"groups"`
			Filename    string  `yaml:"filename"`
			Extension   string  `yaml:"extension"`
			Path        string  `yaml:"path"`
			Archive     string  `yaml:"archive"`
			Format      string  `yaml:"format"`
			Output      string  `yaml:"output"`
			Quiet       bool    `yaml:"quiet"`
		}{
			Domain:      orDefault(o.Domain, DefaultDomain),
			Token:       o.Token,
			IgnoreSSL:   o.IgnoreSSL,
			CAFile:      o.CAFile,
			Concurrency: nonDefault(o.Concurrency, DefaultConcurrency),
			RateLimit:   o.RateLimit,
			RetryCount:  nonDefault(o.RetryCount, DefaultRetryCount),
			LogLevel:    orDefault(o.LogLevel, DefaultLogLevel),
			Groups:      o.Groups,
			Filename:    o.Filename,
			Extension:   o.Extension,
			Path:        o.Path,
			Archive:     orDefault(o.Archive, DefaultArchive),
			Format:      orDefault(o.Format, DefaultFormat),
			Output:      o.Output,
			Quiet:       o.Quiet,
		})
	} else {
		out := struct {
			Domain      string  `yaml:"domain,omitempty"`
			Token       string  `yaml:"token"`
			IgnoreSSL   bool    `yaml:"ignore_ssl,omitempty"`
			CAFile      string  `yaml:"ca_file,omitempty"`
			Concurrency int     `yaml:"concurrency,omitempty"`
			RateLimit   float64 `yaml:"rate_limit,omitempty"`
			RetryCount  int     `yaml:"retry_count,omitempty"`
			LogLevel    string  `yaml:"log_level,omitempty"`
			Groups      string  `yaml:"groups,omitempty"`
			Filename    string  `yaml:"filename,omitempty"`
			Extension   string  `yaml:"extension,omitempty"`
			Path        string  `yaml:"path,omitempty"`
			Archive     string  `yaml:"archive,omitempty"`
			Format      string  `yaml:"format,omitempty"`
			Output      string  `yaml:"output,omitempty"`
			Quiet       bool    `yaml:"quiet,omitempty"`
		}{
			Token:       o.Token,
			Domain:      o.Domain,
			IgnoreSSL:   o.IgnoreSSL,
			CAFile:      o.CAFile,
			Concurrency: o.Concurrency,
			RateLimit:   o.RateLimit,
			RetryCount:  o.RetryCount,
			LogLevel:    o.LogLevel,
			Groups:      o.Groups,
			Filename:    o.Filename,
			Extension:   o.Extension,
			Path:        o.Path,
			Archive:     o.Archive,
			Format:      o.Format,
			Output:      o.Output,
			Quiet:       o.Quiet,
		}
		if out.Domain == DefaultDomain {
			out.Domain = ""
		}
		if out.Concurrency == DefaultConcurrency {
			out.Concurrency = 0
		}
		if out.RetryCount == DefaultRetryCount {
			out.RetryCount = 0
		}
		if out.LogLevel == DefaultLogLevel {
			out.LogLevel = ""
		}
		if out.Archive == DefaultArchive {
			out.Archive = ""
		}
		if out.Format == DefaultFormat {
			out.Format = ""
		}
		raw, err = yaml.Marshal(&out)
	}
	if err != nil {
		return "", err
	}
	path := o.File
	if path == "" {
		path = filepath.Join(o.Dir, FileName)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func nonDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
