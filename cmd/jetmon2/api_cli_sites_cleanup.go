package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type apiSitesCleanupOptions struct {
	batch          string
	siteIDs        apiInt64SliceFlags
	count          int
	blogIDStart    int64
	dryRun         bool
	ignoreNotFound bool
}

type apiSitesCleanupSummary struct {
	DryRun bool                    `json:"dry_run,omitempty"`
	Batch  string                  `json:"batch,omitempty"`
	Count  int                     `json:"count"`
	Sites  []apiSitesCleanupResult `json:"sites"`
}

type apiSitesCleanupResult struct {
	SiteID int64  `json:"site_id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func cmdAPISitesCleanup(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites cleanup", &opts)
	cleanup := apiSitesCleanupOptions{
		count:          apiSitesBulkAddMaxCount,
		ignoreNotFound: true,
	}
	fs.StringVar(&cleanup.batch, "batch", "", "batch label whose deterministic site ids should be deleted")
	fs.Var(&cleanup.siteIDs, "site-id", "explicit site id to delete (repeatable or comma-separated)")
	fs.IntVar(&cleanup.count, "count", cleanup.count, "number of batch-derived site ids to delete, max 200")
	fs.Int64Var(&cleanup.blogIDStart, "blog-id-start", 0, "first batch blog_id; default derives from --batch")
	fs.BoolVar(&cleanup.dryRun, "dry-run", false, "print the planned deletes without sending requests")
	fs.BoolVar(&cleanup.ignoreNotFound, "ignore-not-found", cleanup.ignoreNotFound, "treat 404 responses as already cleaned")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api sites cleanup [flags]")
	}
	return runAPISitesCleanup(context.Background(), nil, opts, cleanup)
}

func runAPISitesCleanup(ctx context.Context, client *http.Client, opts apiCLIOptions, cleanup apiSitesCleanupOptions) error {
	if opts.out == nil {
		opts.out = io.Discard
	}
	siteIDs, err := apiCleanupSiteIDs(cleanup)
	if err != nil {
		return err
	}

	summary := apiSitesCleanupSummary{
		DryRun: cleanup.dryRun,
		Batch:  cleanup.batch,
		Count:  len(siteIDs),
		Sites:  make([]apiSitesCleanupResult, 0, len(siteIDs)),
	}
	for _, siteID := range siteIDs {
		result := apiSitesCleanupResult{SiteID: siteID}
		if cleanup.dryRun {
			result.Status = "would_delete"
			summary.Sites = append(summary.Sites, result)
			continue
		}
		resp, err := doAPIRequest(ctx, client, opts, http.MethodDelete, "/api/v1/sites/"+strconv.FormatInt(siteID, 10), nil)
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			summary.Sites = append(summary.Sites, result)
			_ = writeAPIValueOutput(opts.out, summary, opts)
			return fmt.Errorf("delete site %d: %w", siteID, err)
		}
		switch {
		case resp.StatusCode == http.StatusNotFound && cleanup.ignoreNotFound:
			result.Status = "not_found"
		case resp.StatusCode >= 400:
			result.Status = "failed"
			result.Error = strings.TrimSpace(string(resp.Body))
			if result.Error == "" {
				result.Error = resp.Status
			}
			summary.Sites = append(summary.Sites, result)
			_ = writeAPIValueOutput(opts.out, summary, opts)
			return fmt.Errorf("delete site %d returned %s", siteID, resp.Status)
		default:
			result.Status = "deleted"
		}
		summary.Sites = append(summary.Sites, result)
	}
	return writeAPIValueOutput(opts.out, summary, opts)
}

func apiCleanupSiteIDs(cleanup apiSitesCleanupOptions) ([]int64, error) {
	if cleanup.siteIDs.set {
		return cleanup.siteIDs.valuesOrEmpty(), nil
	}
	if cleanup.batch == "" && cleanup.blogIDStart == 0 {
		return nil, errors.New("use --batch, --blog-id-start, or --site-id")
	}
	if cleanup.count <= 0 {
		return nil, errors.New("count must be positive")
	}
	if cleanup.count > apiSitesBulkAddMaxCount {
		return nil, fmt.Errorf("count must be <= %d", apiSitesBulkAddMaxCount)
	}
	start := cleanup.blogIDStart
	if start == 0 {
		start = apiCLIBatchBlogIDStart(cleanup.batch)
	}
	if start <= 0 {
		return nil, errors.New("blog-id-start must be positive")
	}
	ids := make([]int64, 0, cleanup.count)
	for i := 0; i < cleanup.count; i++ {
		ids = append(ids, start+int64(i))
	}
	return ids, nil
}
