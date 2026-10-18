package cli

import (
	"context"
	"fmt"
	"github.com/peakacom/peaka-cli/internal/api"
	"net/url"
	"time"
)

func refreshRunIDs(started any) ([]string, error) {
	admission, ok := started.(map[string]any)
	if !ok || admission["trackingComplete"] != true {
		return nil, api.Exit(api.ExitFailed, "refresh was submitted but cannot be followed by exact operation ID; inspect metadata operations (no catalog-status fallback)")
	}
	raw, ok := admission["runIds"].([]any)
	if !ok {
		return nil, api.Exit(api.ExitFailed, "refresh response has no operation ID list")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, item := range raw {
		id, ok := item.(string)
		if !ok || id == "" {
			return nil, api.Exit(api.ExitFailed, "invalid refresh operation ID")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}

func refreshRunState(status string) (bool, error) {
	switch status {
	case "done":
		return true, nil
	case "queued", "running", "cancel_requested":
		return false, nil
	case "failed", "partially_completed", "cancelled", "timed_out", "paused", "skipped":
		return true, api.Exit(api.ExitFailed, "metadata refresh ended with status %s; other submitted operations may still be running", status)
	default:
		return true, api.Exit(api.ExitFailed, "unrecognized metadata operation status %q", status)
	}
}

func waitMetadataOperations(client *api.Client, project string, started any, timeout time.Duration) error {
	ids, err := refreshRunIDs(started)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(rt.Ctx, timeout)
	defer cancel()
	finished := map[string]bool{}
	latest := map[string]record{}
	for len(finished) < len(ids) {
		for _, id := range ids {
			if finished[id] {
				continue
			}
			var row record
			err := client.Get(ctx, fmt.Sprintf("/metadata/%s/operations/%s", url.PathEscape(project), url.PathEscape(id)), nil, &row)
			if err != nil {
				return err
			}
			if str(row["id"]) != id || str(row["projectId"]) != project || str(row["kind"]) != "refresh" {
				return api.Exit(api.ExitFailed, "operation identity does not match the submitted refresh")
			}
			latest[id] = row
			done, stateErr := refreshRunState(str(row["status"]))
			if stateErr != nil {
				_ = rt.print(map[string]any{"runIds": ids, "operations": latest})
				return stateErr
			}
			if done {
				finished[id] = true
			}
		}
		if len(finished) == len(ids) {
			break
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return api.Exit(api.ExitRetryable, "stopped waiting for refresh operations: %s", ctx.Err())
		}
	}
	return rt.print(map[string]any{"runIds": ids, "operations": latest})
}
