package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

// exportFlags are shared by `tables export` and `queries export`.
type exportFlags struct {
	format, compression string
	limit               int
	columns             []string
	systemColumns       bool
	csvDelimiter        string
	csvNoHeader         bool
	wait                bool
	out                 string
	timeout             time.Duration
}

func (f *exportFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.format, "format-out", "CSV", "output format: CSV or JSONL")
	cmd.Flags().StringVar(&f.compression, "compression", "", "NONE or GZIP")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "row limit (1..1000000)")
	cmd.Flags().StringSliceVar(&f.columns, "columns", nil, "columns to export (default: all)")
	cmd.Flags().BoolVar(&f.systemColumns, "system-columns", false, "include Peaka system columns (_id, _version, …)")
	cmd.Flags().StringVar(&f.csvDelimiter, "delimiter", "", "CSV field delimiter")
	cmd.Flags().BoolVar(&f.csvNoHeader, "csv-no-header", false, "CSV without a header row")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until the export finishes and print the final record")
	cmd.Flags().StringVarP(&f.out, "out", "O", "", "wait, then download the files into this directory (or file, for a single file); - for stdout")
	cmd.Flags().DurationVar(&f.timeout, "wait-timeout", 30*time.Minute, "how long to wait")
}

func (f *exportFlags) body() map[string]any {
	b := map[string]any{"format": strings.ToUpper(f.format)}
	if f.compression != "" {
		b["compression"] = strings.ToUpper(f.compression)
	}
	if f.limit > 0 {
		b["limit"] = f.limit
	}
	if len(f.columns) > 0 {
		b["columns"] = f.columns
	}
	// Always sent: the API includes system columns unless told not to, and --system-columns says they are opt-in.
	b["includeSystemColumns"] = f.systemColumns
	csv := map[string]any{}
	if f.csvDelimiter != "" {
		csv["delimiter"] = f.csvDelimiter
	}
	if f.csvNoHeader {
		csv["includeHeader"] = false
	}
	if len(csv) > 0 {
		b["csvOptions"] = csv
	}
	return b
}

// startExport POSTs the export and either prints the job or waits/downloads.
func (r *Runtime) startExport(path string, f *exportFlags) error {
	client, err := r.api()
	if err != nil {
		return err
	}
	var job record
	resp, err := client.Do(r.Ctx, http.MethodPost, path, nil, f.body(), &job)
	if err != nil {
		return err
	}
	if resp.Status == 0 { // --dry-run printed the request
		return nil
	}
	if !f.wait && f.out == "" {
		r.note("Export %s %s. `peaka exports describe %s` shows its progress; `peaka exports download %s` fetches the files.", str(job["id"]), str(job["status"]), str(job["id"]), str(job["id"]))
		return r.print(job)
	}
	project, _ := r.needProject()
	done, err := r.waitExport(project, str(job["id"]), f.timeout)
	if err != nil {
		return err
	}
	if f.out != "" {
		return r.downloadExport(done, f.out)
	}
	return r.print(done)
}

// waitExport polls an export until it is finished.
func (r *Runtime) waitExport(project, id string, timeout time.Duration) (record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	delay := 500 * time.Millisecond
	var lastStatus string
	for {
		var job record
		if err := client.Get(r.Ctx, fmt.Sprintf("/data/projects/%s/exports/%s", project, id), nil, &job); err != nil {
			return nil, err
		}
		status := str(job["status"])
		if status != lastStatus && r.StdoutTTY {
			r.note("export %s: %s", id, status)
			lastStatus = status
		}
		switch status {
		case "SUCCEEDED":
			return job, nil
		case "FAILED", "CANCELLED", "EXPIRED":
			return job, api.Exit(api.ExitFailed, "export %s %s: %s", id, strings.ToLower(status), firstNonEmpty(str(job["error"]), "no detail"))
		}
		if time.Now().After(deadline) {
			return job, api.Exit(api.ExitRetryable, "export %s still %s after %s", id, status, timeout)
		}
		select {
		case <-time.After(delay):
		case <-r.Ctx.Done():
			return nil, api.Exit(api.ExitInterrupted, "interrupted")
		}
		if delay < 5*time.Second {
			delay += delay / 2
		}
	}
}

// downloadExport fetches the files of a SUCCEEDED export.
func (r *Runtime) downloadExport(job record, out string) error {
	files, _ := job["files"].([]any)
	if len(files) == 0 {
		if b, _ := job["downloadable"].(bool); !b {
			return api.Exit(api.ExitPermission, "export %s is not downloadable by this identity (only its owner may download it)", str(job["id"]))
		}
		return api.Exit(api.ExitNotFound, "export %s has no files (status %s)", str(job["id"]), str(job["status"]))
	}
	toStdout := out == "-"
	single := false
	if !toStdout {
		if st, err := os.Stat(out); err == nil && st.IsDir() {
			// directory: keep the file names
		} else if len(files) == 1 && (filepath.Ext(out) != "" || err == nil) {
			single = true
		} else if err := os.MkdirAll(out, 0o755); err != nil {
			return err
		}
	}
	for _, fv := range files {
		f, _ := fv.(map[string]any)
		name := str(f["name"])
		u := str(f["url"])
		req, err := http.NewRequestWithContext(r.Ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return &api.Problem{Code: "UPSTREAM_UNAVAILABLE", Detail: fmt.Sprintf("download %s: %v", name, err), Retryable: true}
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return api.Exit(api.ExitFailure, "download %s: HTTP %d (links expire; `peaka exports describe` returns fresh ones)", name, resp.StatusCode)
		}
		var w io.Writer = r.Out
		var file *os.File
		if !toStdout {
			target := filepath.Join(out, name)
			if single {
				target = out
			}
			file, err = os.Create(target)
			if err != nil {
				resp.Body.Close()
				return err
			}
			w = file
		}
		n, err := io.Copy(w, resp.Body)
		resp.Body.Close()
		if file != nil {
			file.Close()
			r.note("%s  %s", file.Name(), humanBytes(n))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func newExportsDownloadCmd() *cobra.Command {
	var out string
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "download ID",
		Short: "Download the files of an export (waits while it is still running)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			job, err := rt.waitExport(project, args[0], wait)
			if err != nil {
				return err
			}
			return rt.downloadExport(job, out)
		},
	}
	cmd.Flags().StringVarP(&out, "out", "O", ".", "directory (or file name for a single file); - for stdout")
	cmd.Flags().DurationVar(&wait, "wait-timeout", 30*time.Minute, "how long to wait for a running export")
	return cmd
}

func newExportsWaitCmd() *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "wait ID",
		Short: "Wait until an export finishes, then print it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			job, err := rt.waitExport(project, args[0], wait)
			if job != nil {
				_ = rt.print(job)
			}
			return err
		},
	}
	cmd.Flags().DurationVar(&wait, "wait-timeout", 30*time.Minute, "how long to wait")
	return cmd
}

func newQueriesExportCmd() *cobra.Command {
	f := &exportFlags{}
	cmd := &cobra.Command{
		Use:               "export ID|NAME",
		ValidArgsFunction: argCompleter(completeQueries),
		Short:             "Export a saved query's result to files (CSV or JSONL)",
		Long: `  peaka queries export daily_revenue --format-out PARQUET --wait
  peaka queries export daily_revenue --out ./exports          # wait and download
  peaka queries export daily_revenue --out - | head             # stream the file to stdout`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveQuery(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			return rt.startExport(fmt.Sprintf("/data/projects/%s/queries/%s/exports", project, p["query"]), f)
		},
	}
	f.bind(cmd)
	return cmd
}

// marshal for debug/notes
var _ = json.Marshal

// skipFinishedExport sends no cancel for an export that has finished: the API answers it with the same empty
// success as a real cancel, and "Cancelled." would be untrue.
func skipFinishedExport(c *callCtx) error {
	project, err := rt.needProject()
	if err != nil {
		return err
	}
	client, err := rt.api()
	if err != nil {
		return err
	}
	var job map[string]any
	if err := client.Get(rt.Ctx, fmt.Sprintf("/data/projects/%s/exports/%s", project, url.PathEscape(c.Params["export"])), nil, &job); err != nil {
		return err
	}
	if status := str(job["status"]); status != "" && status != "PENDING" && status != "RUNNING" {
		rt.note("Export %s has already finished (%s); nothing to cancel.", c.Params["export"], status)
		c.Path = ""
	}
	return nil
}
