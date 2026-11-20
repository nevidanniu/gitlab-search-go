package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDomain(t *testing.T) {
	cases := []struct {
		in      string
		proto   string
		host    string
		wantErr bool
	}{
		{"gitlab.com", "https", "gitlab.com", false},
		{"HTTP://GitLab.Example.com", "http", "gitlab.example.com", false},
		{"https://gitlab.example.com/", "https", "gitlab.example.com", false},
		{"", "https", DefaultDomain, false},
		{"ftp://x", "", "", true},
		{"https://a://b", "", "", true},
	}
	for _, c := range cases {
		proto, host, err := ParseDomain(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseDomain(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDomain(%q): %v", c.in, err)
			continue
		}
		if proto != c.proto || host != c.host {
			t.Errorf("ParseDomain(%q) = %s/%s, want %s/%s", c.in, proto, host, c.proto, c.host)
		}
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("domain: gitlab.internal\ntoken: file-token\nretry_count: 9\nlog_level: warn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "file-token" || cfg.Domain != "gitlab.internal" || cfg.RetryCount != 9 || cfg.LogLevel != slog.LevelWarn {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.Protocol != "https" || cfg.Concurrency != DefaultConcurrency {
		t.Fatalf("cfg=%+v", cfg)
	}

	t.Setenv(EnvToken, "env-token")
	t.Setenv(EnvRetryCount, "1")
	cfg, err = Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "env-token" || cfg.RetryCount != 1 {
		t.Fatalf("env override failed: %+v", cfg)
	}
}

func TestLoadNoToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("domain: gitlab.internal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, nil); err == nil {
		t.Fatal("expected missing token error")
	}

	// a file written by setup without --token keeps the key empty, and searching from it must fail
	empty, err := Write(WriteOptions{Dir: t.TempDir(), Domain: "gitlab.internal"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "token: \"\"") {
		t.Fatalf("setup without a token must write an empty token key, file:\n%s", raw)
	}
	if _, err := Load(empty, nil); err == nil {
		t.Fatal("expected empty token error")
	}
}

func TestLoadFlagOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("domain: gitlab.com\ntoken: file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ssl := false
	cfg, err := Load(path, &Override{Token: "flag-token", Domain: "http://gitlab.corp", IgnoreSSL: &ssl})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "flag-token" || cfg.Protocol != "http" || cfg.Domain != "gitlab.corp" {
		t.Fatalf("cfg=%+v", cfg)
	}

	// flag token alone allows running without any config file (search finds nothing from cwd)
	t.Chdir(t.TempDir())
	t.Setenv(EnvToken, "")
	cfg, err = Load("", &Override{Token: "tok", Domain: "gitlab.corp"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok" || cfg.Domain != "gitlab.corp" || cfg.Protocol != "https" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestLoadPerSearchKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := "token: tok\n" +
		"groups: infra,devops\nfilename: '*.yaml'\nextension: tf\npath: roles\n" +
		"archive: exclude\nformat: json\noutput: res.json\nquiet: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{cfg.Groups, cfg.Filename, cfg.Extension, cfg.Path, cfg.Archive, cfg.Format, cfg.Output}
	for i, v := range want {
		if v == "" {
			t.Errorf("key %d not loaded from yaml: %+v", i, cfg)
		}
	}
	if want[4] != "exclude" || want[5] != "json" || !cfg.Quiet {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestWriteOmitsDefaults(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(WriteOptions{
		Dir: dir, Token: "tok", Domain: DefaultDomain,
		Concurrency: DefaultConcurrency, RetryCount: DefaultRetryCount, RateLimit: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, key := range []string{"domain:", "concurrency:", "retry_count:", "rate_limit:", "ignore_ssl:"} {
		if strings.Contains(s, key) {
			t.Errorf("default key %q should be omitted, file:\n%s", key, s)
		}
	}
	if !strings.Contains(s, "token: tok") {
		t.Errorf("token missing, file:\n%s", s)
	}
}

func TestWriteNonDefaultDomainAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	got, err := Write(WriteOptions{File: path, Token: "tok", Domain: "https://git.internal", IgnoreSSL: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("Write must honor File, got %q", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "domain: https://git.internal") || !strings.Contains(s, "ignore_ssl: true") {
		t.Errorf("non-default values must be written, file:\n%s", s)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protocol != "https" || cfg.Domain != "git.internal" || !cfg.IgnoreSSL {
		t.Fatalf("roundtrip: %+v", cfg)
	}
}

func TestWriteSearchDefaultsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(WriteOptions{
		Dir: dir, Token: "tok", LogLevel: "warn", Groups: "pcp,billing",
		Filename: "*.yaml", Extension: "tf", Path: "roles", Archive: "exclude",
		Format: "json", Output: "found.json", Quiet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("%s:\n%s", err, raw)
	}
	want := []string{cfg.LogLevel.String(), cfg.Groups, cfg.Filename, cfg.Extension, cfg.Path, cfg.Archive, cfg.Format, cfg.Output}
	for i, v := range want {
		if v == "" {
			t.Fatalf("key %d lost in roundtrip, file:\n%s", i, raw)
		}
	}
	if cfg.LogLevel != slog.LevelWarn || cfg.Archive != "exclude" || cfg.Format != "json" || !cfg.Quiet {
		t.Fatalf("cfg=%+v", cfg)
	}
	// default enum values must not clutter a non-full file
	defaults, err := Write(WriteOptions{File: filepath.Join(dir, "d.yaml"), Token: "tok", LogLevel: "info", Archive: "all", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(defaults)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"log_level:", "archive:", "format:"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("default key %q should be omitted, file:\n%s", key, raw)
		}
	}
}

func TestWriteFull(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(WriteOptions{Dir: dir, Token: "tok", Concurrency: DefaultConcurrency, RetryCount: DefaultRetryCount, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, key := range []string{"domain:", "token:", "ignore_ssl:", "ca_file:", "concurrency:", "rate_limit:", "retry_count:", "log_level:", "groups:", "filename:", "extension:", "path:", "archive:", "format:", "output:", "quiet:"} {
		if !strings.Contains(s, key) {
			t.Errorf("full config must contain %q, file:\n%s", key, s)
		}
	}

	// the full file must load back with identical values
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != DefaultConcurrency || cfg.RetryCount != DefaultRetryCount || cfg.LogLevel != slog.LevelInfo || cfg.Domain != DefaultDomain {
		t.Fatalf("roundtrip mismatch: %+v", cfg)
	}
}
