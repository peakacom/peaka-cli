package cli

import (
	"context"
	"errors"
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

// Polling of refresh operations (DEV-4133): starts at 2 s and backs off to 15 s; up to five transient failures in a row
// (no connection, 5xx, 429) are waited out. Variables so tests can shorten them.
var (
	refreshPollInitial    = 2 * time.Second
	refreshPollMax        = 15 * time.Second
	refreshTransientLimit = 5
)

// waitMetadataOperations follows the submitted refresh operations to the end. Whatever ends the wait, the run IDs
// and their last known state are printed first, so they can be followed later (DEV-4133). Exit codes: 0 all done;
// 8 an operation ended other than done; 9 the wait timed out or the API stayed unreachable (the operations go on);
// 130 interrupted.
func waitMetadataOperations(client *api.Client, project string, started any, timeout time.Duration) error {
	ids, err := refreshRunIDs(started)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(rt.Ctx, timeout)
	defer cancel()
	finished := map[string]bool{}
	latest := map[string]record{}
	report := func(result error) error {
		_ = rt.print(map[string]any{"runIds": ids, "operations": latest})
		return result
	}
	stopped := func() error {
		if rt.Ctx.Err() != nil {
			return report(api.Exit(api.ExitInterrupted, "interrupted; the refresh operations go on"))
		}
		return report(api.Exit(api.ExitRetryable, "stopped waiting after %s; the refresh operations go on", timeout))
	}
	interval := refreshPollInitial
	transient := 0
	for len(finished) < len(ids) {
		for _, id := range ids {
			if finished[id] {
				continue
			}
			var row record
			err := client.Get(ctx, fmt.Sprintf("/metadata/%s/operations/%s", url.PathEscape(project), url.PathEscape(id)), nil, &row)
			if err != nil {
				if ctx.Err() != nil {
					return stopped()
				}
				var problem *api.Problem
				if errors.As(err, &problem) && (problem.Retryable || problem.Status == 0 || problem.Status == 429 || problem.Status >= 500) {
					transient++
					if transient >= refreshTransientLimit {
						return report(api.Exit(api.ExitRetryable, "the API did not answer %d times in a row; the refresh operations go on: %s", transient, err))
					}
					continue
				}
				return report(err)
			}
			transient = 0
			if str(row["id"]) != id || str(row["projectId"]) != project || str(row["kind"]) != "refresh" {
				return report(api.Exit(api.ExitFailed, "operation identity does not match the submitted refresh"))
			}
			latest[id] = row
			done, stateErr := refreshRunState(str(row["status"]))
			if stateErr != nil {
				return report(stateErr)
			}
			if done {
				finished[id] = true
			}
		}
		if len(finished) == len(ids) {
			break
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return stopped()
		}
		if interval = interval * 3 / 2; interval > refreshPollMax {
			interval = refreshPollMax
		}
	}
	return rt.print(map[string]any{"runIds": ids, "operations": latest})
}
