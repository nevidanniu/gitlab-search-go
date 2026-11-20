package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab-search-go/internal/config"
)

func TestNextLink(t *testing.T) {
	h := http.Header{}
	h.Set("Link", `<https://gitlab/x?page=2>; rel="next", <https://gitlab/x?page=5>; rel="last"`)
	if got := nextLink(h); got != "https://gitlab/x?page=2" {
		t.Fatalf("got %q", got)
	}
	h.Set("Link", `<https://gitlab/x?page=1>; rel="first"`)
	if got := nextLink(h); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFetchGroupsPaginates(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v4/groups?per_page=100&page=2>; rel="next"`, srv.URL))
			fmt.Fprint(w, `[{"id":1,"name":"g1"}]`)
		case "2":
			fmt.Fprint(w, `[{"id":2,"name":"g2"}]`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	groups, err := c.FetchGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "g1" || groups[1].ID != "2" {
		t.Fatalf("groups=%+v", groups)
	}
}

func TestDecodeHitsBothShapes(t *testing.T) {
	object := []byte(`{"hits":[{"data":"a","filename":"f","ref":"main","startline":1}]}`)
	array := []byte(`[{"data":"a","filename":"f","ref":"main","startline":"12","basename":"f","path":"dir/f"}]`)
	for i, body := range [][]byte{object, array} {
		hits, err := decodeHits(body)
		if err != nil {
			t.Fatalf("shape %d: %v", i, err)
		}
		if len(hits) != 1 || hits[0].Filename != "f" {
			t.Fatalf("shape %d: hits=%+v", i, hits)
		}
	}
	if hits, err := decodeHits(array); err != nil || hits[0].StartLine != 12 {
		t.Fatalf("string startline should decode to 12, got %+v err=%v", hits, err)
	}
}

func TestSearchProjectBlobsKeepsPagesBeforeFailure(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v4/projects/7/search?scope=blobs&per_page=100&page=2>; rel="next"`, srv.URL))
			fmt.Fprint(w, `{"hits":[{"data":"a","filename":"f1","ref":"main","startline":1}]}`)
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, func(cfg *config.Config) { cfg.RetryCount = 1 })
	var delivered int
	err := c.SearchProjectBlobs(context.Background(), 7, "term", func(hits []Hit) error {
		delivered += len(hits)
		return nil
	})
	if err == nil {
		t.Fatal("expected search to fail on page 2")
	}
	if delivered != 1 {
		t.Fatalf("expected page 1 delivered before failure, got %d hits", delivered)
	}
}
