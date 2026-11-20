package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"gitlab-search-go/internal/gitlab"
	"gitlab-search-go/internal/search"
)

// NewWriter resolves the three output modes: console, console+file, file only.
func NewWriter(console io.Writer, file string, quiet bool) (io.Writer, func() error, error) {
	if file == "" {
		return console, nil, nil
	}
	f, err := os.Create(file)
	if err != nil {
		return nil, nil, fmt.Errorf("could not open output file %s: %w", file, err)
	}
	closer := func() error { return f.Close() }
	if quiet {
		return f, closer, nil
	}
	return io.MultiWriter(console, f), closer, nil
}

func Permalink(p gitlab.Project, h gitlab.Hit) string {
	return fmt.Sprintf("%s/blob/%s/%s#L%d", p.WebURL, h.Ref, h.Filename, h.StartLine)
}

func renderPlain(w *bufio.Writer, results []search.Spooled, term string) error {
	matches := 0
	for _, res := range results {
		if res.Hits == 0 {
			continue
		}
		matches += res.Hits
		name := res.Project.Name
		if res.Project.Archived {
			name += " (archived)"
		}
		fmt.Fprintf(w, "%s:\n", name)
		err := forEachHit(res, func(h gitlab.Hit) error {
			fmt.Fprintf(w, "\n\t%s\n\n", Permalink(res.Project, h))
			fmt.Fprintf(w, "\t\t%s\n", strings.ReplaceAll(h.Data, "\n", "\n\t\t"))
			return nil
		})
		if err != nil {
			return err
		}
	}
	if matches == 0 {
		fmt.Fprintf(w, "no matches for %q\n", term)
	}
	return nil
}

type jsonProject struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	WebURL   string `json:"web_url"`
	Archived bool   `json:"archived"`
}

type jsonHit struct {
	gitlab.Hit
	Permalink string `json:"permalink"`
}

// writeResultsJSON streams the results array without ever holding more than one project in memory.
func writeResultsJSON(w *bufio.Writer, results []search.Spooled) error {
	fmt.Fprint(w, "[")
	first := true
	for _, res := range results {
		if res.Hits == 0 {
			continue
		}
		if !first {
			fmt.Fprint(w, ",")
		}
		first = false
		proj, err := json.Marshal(jsonProject{
			ID: res.Project.ID, Name: res.Project.Name,
			WebURL: res.Project.WebURL, Archived: res.Project.Archived,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(w, `{"project":%s,"hits":[`, proj)
		inner := true
		err = forEachHit(res, func(h gitlab.Hit) error {
			if !inner {
				fmt.Fprint(w, ",")
			}
			inner = false
			raw, err := json.Marshal(jsonHit{Hit: h, Permalink: Permalink(res.Project, h)})
			if err != nil {
				return err
			}
			_, err = w.Write(raw)
			return err
		})
		if err != nil {
			return err
		}
		fmt.Fprint(w, "]}")
	}
	fmt.Fprint(w, "]")
	return nil
}

func forEachHit(res search.Spooled, fn func(gitlab.Hit) error) error {
	f, err := os.Open(res.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var h gitlab.Hit
		if err := json.Unmarshal(line, &h); err != nil {
			return fmt.Errorf("reading spooled hits from %s: %w", res.Path, err)
		}
		if err := fn(h); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Quote is one match for the end-of-run report.
type Quote struct {
	Project   string `json:"project"`
	Permalink string `json:"permalink"`
	Line      string `json:"line"`
}

// Quotes returns every match in output order; the report lists them all.
func Quotes(results []search.Spooled, term string) ([]Quote, error) {
	var out []Quote
	for _, res := range results {
		if res.Hits == 0 {
			continue
		}
		err := forEachHit(res, func(h gitlab.Hit) error {
			out = append(out, Quote{
				Project:   res.Project.Name,
				Permalink: Permalink(res.Project, h),
				Line:      quoteLine(h.Data, term),
			})
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// quoteLine prefers the line that really contains the term; GitLab may return context only.
func quoteLine(data, term string) string {
	needle := strings.ToLower(term)
	first := ""
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if first == "" {
			first = trimmed
		}
		if strings.Contains(strings.ToLower(trimmed), needle) {
			return truncateRunes(trimmed, 160)
		}
	}
	return truncateRunes(first, 160)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
