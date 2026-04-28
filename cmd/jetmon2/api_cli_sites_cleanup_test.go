package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunAPISitesCleanupDeletesBatchAndIgnoresMissing(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "000"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "001"):
			writeTestStatusJSON(t, w, http.StatusNotFound, map[string]string{"code": "site_not_found"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	start := apiCLIBatchBlogIDStart("cleanup-batch")
	err := runAPISitesCleanup(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesCleanupOptions{
		batch:          "cleanup-batch",
		count:          2,
		ignoreNotFound: true,
	})
	if err != nil {
		t.Fatalf("runAPISitesCleanup() error = %v\nstdout=%s", err, stdout.String())
	}
	var summary apiSitesCleanupSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, stdout.String())
	}
	if summary.Batch != "cleanup-batch" || summary.Count != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Sites[0].SiteID != start || summary.Sites[0].Status != "deleted" {
		t.Fatalf("first cleanup result = %#v", summary.Sites[0])
	}
	if summary.Sites[1].SiteID != start+1 || summary.Sites[1].Status != "not_found" {
		t.Fatalf("second cleanup result = %#v", summary.Sites[1])
	}
	wantCalls := []string{
		"DELETE /api/v1/sites/" + strconvInt64(start),
		"DELETE /api/v1/sites/" + strconvInt64(start+1),
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func TestRunAPISitesCleanupDryRunTable(t *testing.T) {
	var stdout bytes.Buffer
	err := runAPISitesCleanup(context.Background(), nil, apiCLIOptions{
		output: "table",
		out:    &stdout,
		errOut: ioDiscard{},
	}, apiSitesCleanupOptions{
		siteIDs: mustSiteIDs(t, "42,43"),
		dryRun:  true,
	})
	if err != nil {
		t.Fatalf("runAPISitesCleanup() error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{
		"site_id  status",
		"42       would_delete",
		"43       would_delete",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
}

func TestAPICleanupSiteIDsFromBatch(t *testing.T) {
	ids, err := apiCleanupSiteIDs(apiSitesCleanupOptions{batch: "batch-a", count: 3})
	if err != nil {
		t.Fatalf("apiCleanupSiteIDs() error = %v", err)
	}
	start := apiCLIBatchBlogIDStart("batch-a")
	want := []int64{start, start + 1, start + 2}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids[%d] = %d, want %d", i, ids[i], want[i])
		}
	}
}

func strconvInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
