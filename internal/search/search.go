package search

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"gitlab-search-go/internal/config"
	"gitlab-search-go/internal/gitlab"
)

const TmpDirName = ".gitlab-search-tmp"

// ErrAllProjectsFailed means every search attempt errored and nothing was collected.
var ErrAllProjectsFailed = errors.New("all project searches failed after retries")

type Options struct {
	Term      string
	GroupsCSV string
	Filename  string
	Extension string
	Path      string
	Archive   string
}

// Spooled is one project with its hits written to a temporary JSONL file.
type Spooled struct {
	Project gitlab.Project
	Path    string
	Hits    int
}

type Runner struct {
	client  *gitlab.Client
	cfg     *config.Config
	log     *slog.Logger
	dir     string
	Results []Spooled
	Skipped int
	Groups  int
	Hits    int
}

func NewRunner(client *gitlab.Client, cfg *config.Config, log *slog.Logger) (*Runner, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cwd, TmpDirName)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("could not clean previous %s directory: %w", TmpDirName, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Runner{client: client, cfg: cfg, log: log, dir: dir}, nil
}

func (r *Runner) Cleanup() { os.RemoveAll(r.dir) }

// TmpDir is the hidden directory holding per-project JSONL spool files.
func (r *Runner) TmpDir() string { return r.dir }

func (r *Runner) Run(ctx context.Context, opts Options) error {
	if opts.GroupsCSV == "" {
		r.log.Info("listing groups, this can take a while on large instances")
	}
	groups, err := r.resolveGroups(ctx, opts.GroupsCSV)
	if err != nil {
		return fmt.Errorf("fetching groups: %w", err)
	}
	r.log.Info("groups resolved", "count", len(groups))
	r.Groups = len(groups)
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
	}
	r.log.Debug("Using groups", "groups", strings.Join(names, ", "))

	projects, err := r.resolveProjects(ctx, groups, opts.Archive)
	if err != nil {
		return err
	}
	shortNames := make([]string, len(projects))
	for i, p := range projects {
		shortNames[i] = p.Name
	}
	r.log.Debug("Using projects", "projects", strings.Join(shortNames, ", "))
	r.log.Info("projects resolved", "projects", len(projects))

	if len(projects) == 0 {
		r.log.Warn("no projects found for the selected groups")
		return nil
	}

	r.Results = make([]Spooled, len(projects))
	for i, p := range projects {
		r.Results[i] = Spooled{
			Project: p,
			Path:    filepath.Join(r.dir, fmt.Sprintf("project-%06d.jsonl", i)),
		}
	}

	total := len(projects)
	r.log.Info("search started", "projects", total, "term", opts.Term)

	query := gitlab.BuildSearchQuery(opts.Term, opts.Filename, opts.Extension, opts.Path)
	var skipped, done, hitsSeen atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.cfg.Concurrency)
	for i := range r.Results {
		idx := i
		g.Go(func() error {
			res := &r.Results[idx]
			hits, err := r.searchOne(gctx, res, query)
			if err != nil {
				// Partial pages already on disk are kept and still printed.
				skipped.Add(1)
				r.log.Warn("project skipped after retries", "project", res.Project.Name, "hits_kept", hits, "error", err)
			}
			res.Hits = hits
			n := done.Add(1)
			hitsSeen.Add(int64(hits))
			if n == int64(total) || n%10 == 0 {
				r.log.Info("progress", "projects_done", n, "projects_total", total, "hits_so_far", hitsSeen.Load())
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	r.Skipped = int(skipped.Load())
	if r.Skipped > 0 {
		r.log.Warn("search finished with skipped projects", "skipped", r.Skipped, "total", len(projects))
	}
	totalHits := 0
	for _, res := range r.Results {
		totalHits += res.Hits
	}
	r.Hits = totalHits
	r.log.Info("search finished", "projects", len(projects), "hits", totalHits, "skipped", r.Skipped)
	if totalHits == 0 && r.Skipped == len(projects) {
		return ErrAllProjectsFailed
	}
	return nil
}

func (r *Runner) resolveGroups(ctx context.Context, csv string) ([]gitlab.Group, error) {
	if csv == "" {
		return r.client.FetchGroups(ctx)
	}
	var out []gitlab.Group
	for _, name := range strings.Split(csv, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, gitlab.Group{ID: name, Name: name})
	}
	return out, nil
}

// resolveProjects fetches projects per group in parallel; a group that fails
// after retries is skipped, order follows group order then project order.
func (r *Runner) resolveProjects(ctx context.Context, groups []gitlab.Group, archive string) ([]gitlab.Project, error) {
	perGroup := make([][]gitlab.Project, len(groups))
	var mu sync.Mutex
	failed := 0
	var wg sync.WaitGroup
	for i, gr := range groups {
		wg.Add(1)
		go func(i int, gr gitlab.Group) {
			defer wg.Done()
			projects, err := r.client.FetchProjects(ctx, gr.ID, archive)
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				r.log.Warn("group skipped after retries", "group", gr.Name, "error", err)
				return
			}
			perGroup[i] = projects
			r.log.Info("group projects fetched", "group", gr.Name, "projects", len(projects))
		}(i, gr)
	}
	wg.Wait()
	if len(groups) > 0 && failed == len(groups) {
		return nil, fmt.Errorf("project listing failed for all %d groups", len(groups))
	}

	seen := make(map[int]bool)
	var out []gitlab.Project
	for _, ps := range perGroup {
		for _, p := range ps {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *Runner) searchOne(ctx context.Context, res *Spooled, query string) (int, error) {
	f, err := os.Create(res.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	count := 0
	err = r.client.SearchProjectBlobs(ctx, res.Project.ID, query, func(hits []gitlab.Hit) error {
		for i := range hits {
			raw, err := json.Marshal(&hits[i])
			if err != nil {
				return err
			}
			if _, err := w.Write(append(raw, '\n')); err != nil {
				return err
			}
			count++
		}
		return w.Flush()
	})
	if ferr := w.Flush(); ferr != nil && err == nil {
		err = ferr
	}
	return count, err
}
