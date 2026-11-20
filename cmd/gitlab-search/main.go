package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"gitlab-search-go/internal/config"
	"gitlab-search-go/internal/gitlab"
	"gitlab-search-go/internal/output"
	"gitlab-search-go/internal/search"
)

const version = "1.0.0"

// Allowed values of the enumerated configuration keys.
var (
	archives  = []string{"all", "only", "exclude"}
	formats   = []string{"plain", "json"}
	logLevels = []string{"debug", "info", "warn", "error"}
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

type searchFlags struct {
	groups     string
	filename   string
	extension  string
	path       string
	archive    string
	configFile string
	logLevel   string
	outputFile string
	quiet      bool
	format     string
	baseURL    string
	token      string
	ignoreSSL  bool
	caFile     string
}

func newRootCmd() *cobra.Command {
	f := &searchFlags{}
	root := &cobra.Command{
		Use:           "gitlab-search [options] <search-term>",
		Short:         "Search a string across all GitLab group projects",
		Version:       version,
		Example:       "  # against your own GitLab without a config file\n  gitlab-search --base-url https://gitlab.corp.example --token glpat-xxx here-is-my-search-term\n\n  # persist it once\n  gitlab-search setup --token glpat-xxx --base-url https://gitlab.corp.example",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runSearch(cmd, f, args[0])
		},
	}
	flags := root.Flags()
	flags.StringVarP(&f.groups, "groups", "g", "", "comma-separated group names to search (config key: groups)")
	flags.StringVarP(&f.filename, "filename", "f", "", "only search contents of files matching this glob (config key: filename)")
	flags.StringVarP(&f.extension, "extension", "e", "", "only search files with this extension (config key: extension)")
	flags.StringVarP(&f.path, "path", "p", "", "only search files under this path (config key: path)")
	flags.StringVarP(&f.archive, "archive", "a", "all", "archived projects handling: all, only or exclude (config key: archive)")
	flags.Lookup("archive").NoOptDefVal = "all"
	flags.StringVar(&f.configFile, "config", "", "explicit path to the YAML configuration file")
	flags.StringVar(&f.logLevel, "log-level", "", "log level: debug, info, warn, error")
	flags.StringVarP(&f.outputFile, "output", "o", "", "also write the whole run record to this file (config key: output)")
	flags.BoolVar(&f.quiet, "quiet", false, "do not print results to the console, requires output (config key: quiet)")
	flags.StringVar(&f.format, "format", "plain", "output format: plain or json (config key: format)")
	flags.StringVar(&f.baseURL, "base-url", "", "root URL of your GitLab (with http(s)://), overrides the config file (default https://gitlab.com)")
	flags.StringVar(&f.token, "token", "", "personal access token (read_api), overrides the config file")
	flags.BoolVar(&f.ignoreSSL, "ignore-ssl", false, "skip TLS certificate verification (prefer --ca-file for private CAs)")
	flags.StringVar(&f.caFile, "ca-file", "", "path to a PEM CA bundle used for TLS verification")
	root.AddCommand(newSetupCmd())
	return root
}

func runSearch(cmd *cobra.Command, f *searchFlags, term string) error {
	ctx := cmd.Context()

	ov := &config.Override{}
	if f.baseURL != "" {
		ov.Domain = f.baseURL
	}
	if f.token != "" {
		ov.Token = f.token
	}
	if f.caFile != "" {
		ov.CAFile = f.caFile
	}
	if cmd.Flags().Changed("ignore-ssl") {
		b := f.ignoreSSL
		ov.IgnoreSSL = &b
	}
	cfg, err := config.Load(f.configFile, ov)
	if err != nil {
		return err
	}
	applyLogLevelOverride(cfg, f.logLevel)

	// CLI flag > configuration file > built-in default
	flags := cmd.Flags()
	res := func(name, flagVal, cfgVal, def string) string {
		if flags.Changed(name) {
			return flagVal
		}
		if cfgVal != "" {
			return cfgVal
		}
		return def
	}
	groups := res("groups", f.groups, cfg.Groups, "")
	filename := res("filename", f.filename, cfg.Filename, "")
	extension := res("extension", f.extension, cfg.Extension, "")
	path := res("path", f.path, cfg.Path, "")
	archive := res("archive", f.archive, cfg.Archive, "all")
	format := res("format", f.format, cfg.Format, "plain")
	outputFile := res("output", f.outputFile, cfg.Output, "")
	quiet := cfg.Quiet
	if flags.Changed("quiet") {
		quiet = f.quiet
	}

	if !oneOf(format, formats) {
		return fmt.Errorf("invalid format %q, must be one of: %s", format, strings.Join(formats, ", "))
	}
	if !oneOf(archive, archives) {
		return fmt.Errorf("invalid archive %q, must be one of: %s", archive, strings.Join(archives, ", "))
	}
	if quiet && outputFile == "" {
		return fmt.Errorf("quiet mode only makes sense together with --output <file> or the output config key")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	client, err := gitlab.New(cfg, log)
	if err != nil {
		return err
	}
	w, closer, err := output.NewWriter(os.Stdout, outputFile, quiet)
	if err != nil {
		return err
	}
	defer func() {
		if closer != nil {
			_ = closer()
		}
	}()

	params := output.Parameters{
		Version:     version,
		Term:        term,
		Config:      cfg.ConfigPath,
		API:         client.BaseURL,
		Token:       cfg.Token,
		IgnoreSSL:   cfg.IgnoreSSL,
		CAFile:      cfg.CAFile,
		Groups:      groups,
		Filename:    filename,
		Extension:   extension,
		Path:        path,
		Archive:     archive,
		Format:      format,
		Output:      outputFile,
		Quiet:       quiet,
		Concurrency: cfg.Concurrency,
		RateLimit:   cfg.RateLimit,
		RetryCount:  cfg.RetryCount,
		RetryLadder: retryLadderText(cfg.RetryCount),
		LogLevel:    strings.ToLower(cfg.LogLevel.String()),
	}
	record := output.NewRecord(w, format)
	if err := record.Parameters(params); err != nil {
		return err
	}
	if quiet {
		// the record goes only to the file, so the console still shows where the run started
		output.WriteParameters(os.Stderr, params)
	}

	runner, err := search.NewRunner(client, cfg, log)
	if err != nil {
		return err
	}
	defer runner.Cleanup()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	runErr := runner.Run(ctx, search.Options{
		Term:      term,
		GroupsCSV: groups,
		Filename:  filename,
		Extension: extension,
		Path:      path,
		Archive:   archive,
	})

	// Matches and report are written even when some projects failed, so the destination holds
	// the same complete run record the console shows.
	if err := record.Matches(runner.Results, term); err != nil && runErr == nil {
		runErr = err
	}
	quotes, quoteErr := output.Quotes(runner.Results, term)
	if quoteErr != nil {
		log.Warn("could not collect citations for the report", "error", quoteErr)
	}
	report := output.Report{
		Term:        term,
		Filters:     output.Filters(filename, extension, path, archive),
		Groups:      runner.Groups,
		Projects:    len(runner.Results),
		Skipped:     runner.Skipped,
		Hits:        runner.Hits,
		Elapsed:     time.Since(started),
		Destination: output.Destination(outputFile, quiet) + " (" + format + ")",
		Quotes:      quotes,
	}
	if runErr != nil {
		report.Error = runErr.Error()
	}
	if err := record.Report(report); err != nil && runErr == nil {
		runErr = err
	}
	if quiet {
		output.WriteReport(os.Stderr, report)
	}
	if runErr != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return runErr
}

// applyLogLevelOverride implements precedence: flag > environment > config file,
// with the legacy DEBUG variable (any value) enabling debug when nothing else is set.
func applyLogLevelOverride(cfg *config.Config, flagValue string) {
	if flagValue != "" {
		var lvl slog.Level
		if lvl.UnmarshalText([]byte(flagValue)) == nil {
			cfg.LogLevel = lvl
			return
		}
		fmt.Fprintf(os.Stderr, "Invalid --log-level %q, keeping the configured level.\n", flagValue)
		return
	}
	if _, ok := os.LookupEnv(config.EnvLogLevel); ok {
		return
	}
	if _, ok := os.LookupEnv(config.EnvLegacyDebug); ok {
		cfg.LogLevel = slog.LevelDebug
	}
}

type setupFlags struct {
	token       string
	baseURL     string
	ignoreSSL   bool
	caFile      string
	dir         string
	file        string
	concurrency string
	retryCount  string
	rateLimit   string
	logLevel    string
	groups      string
	filename    string
	extension   string
	path        string
	archive     string
	format      string
	output      string
	quiet       bool
	full        bool
}

func newSetupCmd() *cobra.Command {
	f := &setupFlags{}
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "create the YAML configuration file",
		Long: "create the YAML configuration file; every value has a default, so the command works " +
			"without any flag and leaves the token empty for the search to report it",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("the personal access token is a flag now: setup --token %q", args[0])
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(f)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&f.token, "token", "", "personal access token (read_api); left empty in the file when omitted")
	flags.StringVar(&f.baseURL, "base-url", config.DefaultDomain, "root URL of GitLab, with http:// prefix for HTTP")
	flags.BoolVar(&f.ignoreSSL, "ignore-ssl", false, "skip TLS certificate verification (prefer --ca-file for private CAs)")
	flags.StringVar(&f.caFile, "ca-file", "", "path to a PEM CA bundle used for TLS verification")
	flags.StringVar(&f.dir, "dir", ".", "directory to save the configuration file in")
	flags.StringVar(&f.file, "config-file", "", "full path of the configuration file to create (overrides --dir)")
	flags.StringVar(&f.concurrency, "concurrency", "25", "maximum concurrent API requests")
	flags.StringVar(&f.retryCount, "retry-count", "5", "retry count for 429/5xx/network failures")
	flags.StringVar(&f.rateLimit, "rate-limit", "0", "requests per second, 0 disables throttling")
	flags.StringVar(&f.logLevel, "log-level", config.DefaultLogLevel, "log level: debug, info, warn, error")
	flags.StringVarP(&f.groups, "groups", "g", "", "comma-separated group names to search (config key: groups)")
	flags.StringVarP(&f.filename, "filename", "f", "", "only search contents of files matching this glob (config key: filename)")
	flags.StringVarP(&f.extension, "extension", "e", "", "only search files with this extension (config key: extension)")
	flags.StringVarP(&f.path, "path", "p", "", "only search files under this path (config key: path)")
	flags.StringVarP(&f.archive, "archive", "a", config.DefaultArchive, "archived projects handling: all, only or exclude (config key: archive)")
	flags.StringVar(&f.format, "format", config.DefaultFormat, "output format: plain or json (config key: format)")
	flags.StringVarP(&f.output, "output", "o", "", "write the whole run record to this file (config key: output)")
	flags.BoolVar(&f.quiet, "quiet", false, "do not print results to the console, requires output (config key: quiet)")
	flags.BoolVar(&f.full, "full", false, "write every supported key with its resolved value, including defaults")
	return cmd
}

func runSetup(f *setupFlags) error {
	if _, _, err := config.ParseDomain(f.baseURL); err != nil {
		return err
	}
	if f.quiet && strings.TrimSpace(f.output) == "" {
		fmt.Fprintln(os.Stderr, "Warning: quiet is enabled without --output, a search using this config will fail until output is set as well.")
	}
	opts := config.WriteOptions{
		Dir:         f.dir,
		File:        f.file,
		Token:       f.token,
		Domain:      f.baseURL,
		IgnoreSSL:   f.ignoreSSL,
		CAFile:      f.caFile,
		Concurrency: parseSetupInt(f.concurrency, "--concurrency", config.DefaultConcurrency),
		RetryCount:  parseSetupInt(f.retryCount, "--retry-count", config.DefaultRetryCount),
		RateLimit:   parseSetupFloat(f.rateLimit, "--rate-limit", config.DefaultRateLimit),
		LogLevel:    parseSetupChoice(f.logLevel, "--log-level", logLevels, config.DefaultLogLevel),
		Groups:      f.groups,
		Filename:    f.filename,
		Extension:   f.extension,
		Path:        f.path,
		Archive:     parseSetupChoice(f.archive, "--archive", archives, config.DefaultArchive),
		Format:      parseSetupChoice(f.format, "--format", formats, config.DefaultFormat),
		Output:      f.output,
		Quiet:       f.quiet,
		Full:        f.full,
	}
	path, err := config.Write(opts)
	if err != nil {
		return err
	}
	fmt.Printf("\u2714 Successfully wrote config to %s", path)
	if strings.TrimSpace(f.token) == "" {
		fmt.Printf(" with an empty token - add it with \"setup --token <pat>\", the %s variable or the token key\n", config.EnvToken)
		return nil
	}
	fmt.Printf(", gitlab-search is now ready to be used\n")
	return nil
}

// retryLadderText lists the waits between repeats that retryCount actually allows.
func retryLadderText(retryCount int) string {
	if retryCount == 0 {
		return "no repeats"
	}
	ladder := gitlab.DefaultRetryLadder
	shown := min(retryCount, len(ladder))
	steps := make([]string, 0, shown)
	for i := 0; i < shown; i++ {
		steps = append(steps, secondsText(ladder[i]))
	}
	out := strings.Join(steps, ", ")
	if retryCount > len(ladder) {
		out += ", then " + secondsText(ladder[len(ladder)-1])
	}
	return out
}

func secondsText(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// parseSetupChoice keeps the generated file valid by falling back to the default value.
func parseSetupChoice(raw, flagName string, allowed []string, def string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	if oneOf(raw, allowed) {
		return raw
	}
	fmt.Fprintf(os.Stderr, "Invalid value (%s) provided to %s, will be using %s instead.\n", raw, flagName, def)
	return def
}

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}

func parseSetupInt(raw, flagName string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		fmt.Fprintf(os.Stderr, "Invalid number value (%s) provided to %s, will be using %d instead.\n", raw, flagName, def)
		return def
	}
	return n
}

func parseSetupFloat(raw, flagName string, def float64) float64 {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		fmt.Fprintf(os.Stderr, "Invalid number value (%s) provided to %s, will be using %g instead.\n", raw, flagName, def)
		return def
	}
	return v
}
