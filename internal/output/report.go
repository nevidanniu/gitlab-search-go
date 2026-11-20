package output

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Parameters is the resolved run configuration, echoed before the first request is made.
type Parameters struct {
	Version     string
	Term        string
	Config      string
	API         string
	Token       string
	IgnoreSSL   bool
	CAFile      string
	Groups      string
	Filename    string
	Extension   string
	Path        string
	Archive     string
	Format      string
	Output      string
	Quiet       bool
	Concurrency int
	RateLimit   float64
	RetryCount  int
	RetryLadder string
	LogLevel    string
}

func WriteParameters(w io.Writer, p Parameters) {
	fmt.Fprintln(w, "──── run parameters ────")
	fmt.Fprintf(w, "version:   %s\n", p.Version)
	fmt.Fprintf(w, "term:      %q\n", p.Term)
	fmt.Fprintf(w, "config:    %s\n", orNone(p.Config))
	fmt.Fprintf(w, "api:       %s\n", p.API)
	fmt.Fprintf(w, "token:     %s\n", maskToken(p.Token))
	fmt.Fprintf(w, "tls:       %s\n", p.tlsMode())
	fmt.Fprintf(w, "groups:    %s\n", p.groupsMode())
	fmt.Fprintf(w, "filters:   %s\n", Filters(p.Filename, p.Extension, p.Path, p.Archive))
	fmt.Fprintf(w, "output:    %s\n", Destination(p.Output, p.Quiet)+" ("+p.Format+")")
	fmt.Fprintf(w, "limits:    concurrency=%d rate_limit=%g/s retry_count=%d (%s) log_level=%s\n",
		p.Concurrency, p.RateLimit, p.RetryCount, p.RetryLadder, p.LogLevel)
}

func (p Parameters) tlsMode() string {
	switch {
	case p.IgnoreSSL:
		return "certificate checks SKIPPED (--ignore-ssl)"
	case p.CAFile != "":
		return "custom CA " + p.CAFile
	default:
		return "system CAs, certificate checks on"
	}
}

func (p Parameters) groupsMode() string {
	if p.Groups == "" {
		return "all groups visible to the token"
	}
	return p.Groups
}

type jsonParameters struct {
	Version     string  `json:"version"`
	Term        string  `json:"term"`
	Config      string  `json:"config"`
	API         string  `json:"api"`
	Token       string  `json:"token"`
	TLS         string  `json:"tls"`
	Groups      string  `json:"groups"`
	Filters     string  `json:"filters"`
	Output      string  `json:"output"`
	Format      string  `json:"format"`
	Concurrency int     `json:"concurrency"`
	RateLimit   float64 `json:"rate_limit"`
	RetryCount  int     `json:"retry_count"`
	RetryLadder string  `json:"retry_ladder"`
	LogLevel    string  `json:"log_level"`
}

func (p Parameters) asJSON() jsonParameters {
	return jsonParameters{
		Version: p.Version, Term: p.Term, Config: orNone(p.Config), API: p.API,
		Token: maskToken(p.Token), TLS: p.tlsMode(), Groups: p.groupsMode(),
		Filters: Filters(p.Filename, p.Extension, p.Path, p.Archive),
		Output:  Destination(p.Output, p.Quiet), Format: p.Format,
		Concurrency: p.Concurrency, RateLimit: p.RateLimit,
		RetryCount: p.RetryCount, RetryLadder: p.RetryLadder, LogLevel: p.LogLevel,
	}
}

// Filters describes the search restrictions in one line.
func Filters(filename, extension, path, archive string) string {
	if archive == "" {
		archive = "all"
	}
	parts := []string{}
	for _, kv := range [][2]string{{"filename", filename}, {"extension", extension}, {"path", path}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(append(parts, "archive="+archive), "  ")
}

// Destination describes where the results are written.
func Destination(outputFile string, quiet bool) string {
	switch {
	case outputFile == "":
		return "console"
	case quiet:
		return "file " + outputFile + " only"
	default:
		return "console and file " + outputFile
	}
}

// Report is the summary written after the results.
type Report struct {
	Term        string
	Filters     string
	Groups      int
	Projects    int
	Skipped     int
	Hits        int
	Elapsed     time.Duration
	Destination string
	Error       string
	Quotes      []Quote
}

func WriteReport(w io.Writer, r Report) {
	fmt.Fprintln(w, "──── report ────")
	fmt.Fprintf(w, "searched:  %q\n", r.Term)
	if r.Filters != "" {
		fmt.Fprintf(w, "filters:   %s\n", r.Filters)
	}
	fmt.Fprintf(w, "groups:    %d\n", r.Groups)
	fmt.Fprintf(w, "projects:  %d searched, %d skipped after retries\n", r.Projects, r.Skipped)
	fmt.Fprintf(w, "matches:   %d\n", r.Hits)
	fmt.Fprintf(w, "time:      %s\n", r.elapsedText())
	fmt.Fprintf(w, "results:   %s\n", r.Destination)
	if r.Error != "" {
		fmt.Fprintf(w, "error:     %s\n", r.Error)
	}
	if r.Hits == 0 {
		fmt.Fprintln(w, "no matches found")
		return
	}
	fmt.Fprintf(w, "citations (%d):\n", len(r.Quotes))
	for _, q := range r.Quotes {
		fmt.Fprintf(w, "  %s\n      %s\n", q.Permalink, q.Line)
	}
}

func (r Report) elapsedText() string {
	return r.Elapsed.Round(time.Second).String()
}

type jsonReport struct {
	Term      string  `json:"term"`
	Filters   string  `json:"filters,omitempty"`
	Groups    int     `json:"groups"`
	Projects  int     `json:"projects_searched"`
	Skipped   int     `json:"projects_skipped_after_retries"`
	Matches   int     `json:"matches"`
	Elapsed   string  `json:"elapsed"`
	WrittenTo string  `json:"written_to"`
	Error     string  `json:"error,omitempty"`
	Citations []Quote `json:"citations,omitempty"`
}

func (r Report) asJSON() jsonReport {
	return jsonReport{
		Term: r.Term, Filters: r.Filters, Groups: r.Groups, Projects: r.Projects,
		Skipped: r.Skipped, Matches: r.Hits, Elapsed: r.elapsedText(),
		WrittenTo: r.Destination, Error: r.Error, Citations: r.Quotes,
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// maskToken keeps the secret unprintable while still showing which token is in use.
func maskToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "(empty)"
	}
	if len(t) <= 6 {
		return strings.Repeat("*", len(t))
	}
	return t[:3] + strings.Repeat("*", len(t)-6) + t[len(t)-3:]
}
