package search_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab-search-go/internal/config"
	"gitlab-search-go/internal/gitlab"
	"gitlab-search-go/internal/output"
	"gitlab-search-go/internal/search"
)

type mock struct {
	srv         *httptest.Server
	groupCalled atomic.Bool
}

func newMock(t *testing.T) *mock {
	m := &mock{}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups", func(w http.ResponseWriter, r *http.Request) {
		m.groupCalled.Store(true)
		fmt.Fprint(w, `[{"id":1,"name":"grp"}]`)
	})
	mux.HandleFunc("/api/v4/groups/1/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":10,"name":"p-ok","web_url":"%s/p-ok","archived":false},`+
			`{"id":11,"name":"p-bad","web_url":"%s/p-bad","archived":true}]`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/api/v4/projects/10/search", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"hits":[{"data":"line1\nline2 needle","filename":"a/b.go","ref":"main","startline":3}]}`)
	})
	mux.HandleFunc("/api/v4/projects/11/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	m.srv = httptest.NewServer(mux)
	srv = m.srv
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mock) runner(t *testing.T) *search.Runner {
	t.Helper()
	cfgPath := t.TempDir() + "/.gitlab-search.yaml"
	if err := os.WriteFile(cfgPath, []byte("domain: "+m.srv.URL+"\ntoken: t\nretry_count: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := gitlab.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	client.RetryLadder = []time.Duration{5 * time.Millisecond}
	runner, err := search.NewRunner(client, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Cleanup)
	return runner
}

func TestRunPartialSuccessOn429(t *testing.T) {
	m := newMock(t)
	runner := m.runner(t)
	term := "needle"

	err := runner.Run(context.Background(), search.Options{Term: term, Archive: "all"})
	if err != nil {
		t.Fatalf("run should survive a failing project, got %v", err)
	}
	if runner.Skipped != 1 {
		t.Fatalf("expected 1 skipped project, got %d", runner.Skipped)
	}
	if len(runner.Results) != 2 || runner.Results[0].Hits != 1 {
		t.Fatalf("results=%+v", runner.Results)
	}

	var buf bytes.Buffer
	rec := output.NewRecord(&buf, "plain")
	if err := rec.Matches(runner.Results, term); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"p-ok:", "a/b.go", "#L3", "line2 needle", "line1"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "p-bad") {
		t.Errorf("failed project should not be printed:\n%s", out)
	}

	// the whole run record must stay valid JSON and carry parameters, matches and report
	buf.Reset()
	rec = output.NewRecord(&buf, "json")
	if err := rec.Parameters(output.Parameters{Token: "glpat-secrettoken", Format: "json", Concurrency: 25}); err != nil {
		t.Fatal(err)
	}
	if err := rec.Matches(runner.Results, term); err != nil {
		t.Fatal(err)
	}
	quotes, err := output.Quotes(runner.Results, term)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Report(output.Report{Term: term, Hits: runner.Hits, Error: "1 project skipped", Quotes: quotes}); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Parameters struct {
			Token string `json:"token"`
		} `json:"parameters"`
		Results []struct {
			Project struct {
				Name     string `json:"name"`
				Archived bool   `json:"archived"`
			} `json:"project"`
			Hits []struct {
				Filename  string `json:"filename"`
				Permalink string `json:"permalink"`
			} `json:"hits"`
		} `json:"results"`
		Report struct {
			Matches   int    `json:"matches"`
			Error     string `json:"error"`
			Citations []struct {
				Permalink string `json:"permalink"`
			} `json:"citations"`
		} `json:"report"`
	}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON %v:\n%s", err, buf.String())
	}
	if len(parsed.Results) != 1 || parsed.Results[0].Project.Name != "p-ok" ||
		parsed.Results[0].Hits[0].Permalink == "" {
		t.Fatalf("json=%s", buf.String())
	}
	if parsed.Parameters.Token != "glp***********ken" || parsed.Report.Matches != runner.Hits ||
		parsed.Report.Error == "" || len(parsed.Report.Citations) != 1 {
		t.Fatalf("record=%s", buf.String())
	}
}

func TestTmpDirRemovedOnCleanup(t *testing.T) {
	m := newMock(t)
	runner := m.runner(t)

	if err := runner.Run(context.Background(), search.Options{Term: "needle", Archive: "all"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runner.TmpDir()); err != nil {
		t.Fatalf("tmp dir should exist during run: %v", err)
	}
	runner.Cleanup()
	if _, err := os.Stat(runner.TmpDir()); !os.IsNotExist(err) {
		t.Fatal("tmp dir should be removed after cleanup")
	}
}

func TestGroupsFlagSkipsGroupListing(t *testing.T) {
	m := newMock(t)
	runner := m.runner(t)

	if err := runner.Run(context.Background(), search.Options{Term: "needle", GroupsCSV: "1", Archive: "all"}); err != nil {
		t.Fatal(err)
	}
	if m.groupCalled.Load() {
		t.Fatal("explicit groups must not trigger /groups listing")
	}
	if runner.Results[0].Hits != 1 {
		t.Fatalf("expected hits via -g, results=%+v", runner.Results)
	}
}
