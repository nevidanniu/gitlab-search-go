package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	WebURL   string `json:"web_url"`
	Archived bool   `json:"archived"`
}

// Hit is one blob-search result, JSON tags match the GitLab API field names.
type Hit struct {
	Data      string `json:"data"`
	Filename  string `json:"filename"`
	Ref       string `json:"ref"`
	StartLine int    `json:"startline"`
}

// UnmarshalJSON tolerates older GitLab versions that return startline as a string.
func (h *Hit) UnmarshalJSON(raw []byte) error {
	var aux struct {
		Data      string          `json:"data"`
		Filename  string          `json:"filename"`
		Ref       string          `json:"ref"`
		StartLine json.RawMessage `json:"startline"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return err
	}
	h.Data, h.Filename, h.Ref = aux.Data, aux.Filename, aux.Ref
	if len(aux.StartLine) == 0 || string(aux.StartLine) == "null" {
		return nil
	}
	var num int
	if err := json.Unmarshal(aux.StartLine, &num); err == nil {
		h.StartLine = num
		return nil
	}
	var str string
	if err := json.Unmarshal(aux.StartLine, &str); err != nil {
		return fmt.Errorf("decoding startline %s: %w", aux.StartLine, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(str))
	if err != nil {
		return fmt.Errorf("decoding startline %q: %w", str, err)
	}
	h.StartLine = n
	return nil
}

// FetchGroups lists all groups visible to the token, following Link pagination.
func (c *Client) FetchGroups(ctx context.Context) ([]Group, error) {
	var out []Group
	err := c.paginate(ctx, c.BaseURL+"/groups?per_page=100", func(body []byte) error {
		var raw []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("decoding groups: %w", err)
		}
		for _, g := range raw {
			out = append(out, Group{ID: strconv.Itoa(g.ID), Name: g.Name})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FetchProjects lists projects of one group. archived is "all", "only" or "exclude".
func (c *Client) FetchProjects(ctx context.Context, groupID, archived string) ([]Project, error) {
	u := fmt.Sprintf("%s/groups/%s/projects?per_page=100", c.BaseURL, url.PathEscape(groupID))
	switch archived {
	case "only":
		u += "&archived=true"
	case "exclude":
		u += "&archived=false"
	}
	var out []Project
	err := c.paginate(ctx, u, func(body []byte) error {
		var raw []Project
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("decoding projects: %w", err)
		}
		out = append(out, raw...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SearchProjectBlobs searches one project, calling onHits per page so results
// can be streamed to disk. Pages fetched before a failure are already delivered.
// GitLab >= 13.6 returns {"hits":[...]}; older instances return a bare array.
func (c *Client) SearchProjectBlobs(ctx context.Context, projectID int, query string, onHits func([]Hit) error) error {
	u := fmt.Sprintf("%s/projects/%d/search?scope=blobs&per_page=100&search=%s",
		c.BaseURL, projectID, url.QueryEscape(query))
	return c.paginate(ctx, u, func(body []byte) error {
		hits, err := decodeHits(body)
		if err != nil {
			return fmt.Errorf("decoding search results: %w", err)
		}
		if len(hits) == 0 {
			return nil
		}
		return onHits(hits)
	})
}

func decodeHits(body []byte) ([]Hit, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var hits []Hit
		if err := json.Unmarshal(trimmed, &hits); err != nil {
			return nil, err
		}
		return hits, nil
	}
	var raw struct {
		Hits []Hit `json:"hits"`
	}
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, err
	}
	return raw.Hits, nil
}

// BuildSearchQuery embeds GitLab blob-search filters into the search value,
// mirroring the original CLI: "<term> filename:<f> extension:<e> path:<p>".
func BuildSearchQuery(term, filename, extension, path string) string {
	var b strings.Builder
	b.WriteString(term)
	if filename != "" {
		fmt.Fprintf(&b, " filename:%s", filename)
	}
	if extension != "" {
		fmt.Fprintf(&b, " extension:%s", extension)
	}
	if path != "" {
		fmt.Fprintf(&b, " path:%s", path)
	}
	return b.String()
}

func (c *Client) paginate(ctx context.Context, startURL string, handlePage func([]byte) error) error {
	u := startURL
	for u != "" {
		body, hdr, err := c.Get(ctx, u)
		if err != nil {
			return err
		}
		if err := handlePage(body); err != nil {
			return err
		}
		u = nextLink(hdr)
	}
	return nil
}

// nextLink extracts the rel="next" target from the Link response header.
func nextLink(h http.Header) string {
	for _, entry := range strings.Split(h.Get("Link"), ",") {
		parts := strings.Split(entry, ";")
		if len(parts) < 2 {
			continue
		}
		rel := strings.TrimSpace(parts[1])
		if rel == `rel="next"` || rel == "rel=next" {
			target := strings.TrimSpace(parts[0])
			target = strings.TrimPrefix(target, "<")
			target = strings.TrimSuffix(target, ">")
			return target
		}
	}
	return ""
}
