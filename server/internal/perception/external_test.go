package perception

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGetExternalChangesIncludesUnknownAttribution(t *testing.T) {
	bundler := NewBundler(1, nil)
	var (
		mu      sync.Mutex
		queries []string
	)
	bundler.client.http = &http.Client{Transport: externalRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		sql := r.Form.Get("sql")
		mu.Lock()
		queries = append(queries, sql)
		mu.Unlock()

		rows := "[]"
		if strings.Contains(sql, "file_modified") {
			rows = `[[1000,"file_modified","/repo/file.go","unknown"]]`
		}
		body := `{"code":0,"output":[{"records":{"rows":` + rows + `}}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	changes, err := bundler.GetExternalChanges(context.Background(), "tma1", time.UnixMilli(1))
	if err != nil {
		t.Fatal(err)
	}
	if changes == nil || changes.ExternalCount != 1 || len(changes.ExternalChanges) != 1 {
		t.Fatalf("unexpected external changes: %+v", changes)
	}
	if changes.ExternalChanges[0].Attribution != "unknown" {
		t.Fatalf("attribution = %q, want unknown", changes.ExternalChanges[0].Attribution)
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, sql := range queries {
		if strings.Contains(sql, "attribution IN ('human','unknown')") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("external query did not include unknown attribution: %v", queries)
	}
}

type externalRoundTripFunc func(*http.Request) (*http.Response, error)

func (f externalRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
