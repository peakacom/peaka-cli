package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/oauth"
)

// Connectors that read files the account picks (Google Sheets today): a
// catalog needs the picked files in extraParameters.sheets. The studio opens
// Google's file picker for that, with the connection's own Google token. The
// CLI opens the studio's picker page in the browser, which sends the picks to
// the loopback address the CLI listens on; the token never leaves the studio.
// Scripts and agents pass --sheet ID|URL[=name] instead.

// pickedFile is one picked spreadsheet, as the catalog wants it.
type pickedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var sheetURL = regexp.MustCompile(`/spreadsheets/d/([A-Za-z0-9_-]+)`)

// parseSheetFlag reads "ID", "ID=name" or a Google Sheets URL (its query and
// fragment carry '=' of their own, so a URL names the sheet after its id).
func parseSheetFlag(v string) (pickedFile, error) {
	ref, name := strings.TrimSpace(v), ""
	if m := sheetURL.FindStringSubmatch(ref); m != nil {
		ref = m[1]
	} else if i := strings.LastIndex(ref, "="); i > 0 {
		ref, name = ref[:i], ref[i+1:]
	}
	id := ref
	if id == "" || strings.ContainsAny(id, "/ ?") {
		return pickedFile{}, api.Exit(api.ExitUsage, "--sheet wants a spreadsheet id or its Google Sheets URL, optionally =name: %q", v)
	}
	if name == "" {
		name = id
	}
	return pickedFile{ID: id, Name: name}, nil
}

// sheetsSetter puts --sheet values into extraParameters.sheets.
func sheetsSetter(v any, body map[string]any) {
	vals, _ := v.([]string)
	if len(vals) == 0 {
		return
	}
	var files []pickedFile
	for _, s := range vals {
		f, err := parseSheetFlag(s)
		if err != nil {
			continue // reported, with the flag named, when the request is prepared
		}
		files = append(files, f)
	}
	extra, _ := body["extraParameters"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	extra["sheets"] = files
	body["extraParameters"] = extra
}

// prepareFilePicks completes a catalog request for a connector that reads
// picked files: validates --sheet, or opens the picker on a terminal.
func prepareFilePicks(c *callCtx) error {
	if !strings.EqualFold(c.Params["connectionType"], "google_sheets") {
		return nil
	}
	if extra, ok := c.Body["extraParameters"].(map[string]any); ok {
		if files, ok := extra["sheets"].([]pickedFile); ok && len(files) > 0 {
			return nil
		}
		if raw, ok := extra["sheets"]; ok && raw != nil {
			return nil // given through -f extraParameters:=…, as is
		}
	}
	if gf.noInput || gf.dryRun {
		return api.Exit(api.ExitInvalid, "a Google Sheets catalog needs the spreadsheets it reads: --sheet ID|URL[=name] (repeatable), or pick them with `peaka connections pick %s`", c.Params["connectionName"])
	}
	files, err := rt.pickFiles(c.Params["connection"], c.Params["connectionName"])
	if err != nil {
		return err
	}
	extra, _ := c.Body["extraParameters"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	extra["sheets"] = files
	c.Body["extraParameters"] = extra
	return nil
}

// pickFiles opens the studio's picker page for the connection in the browser
// and waits for the picks on the loopback address.
func (r *Runtime) pickFiles(connectionID, connectionName string) ([]pickedFile, error) {
	project, err := r.needProject()
	if err != nil {
		return nil, err
	}
	if r.Settings.Studio == "" {
		return nil, api.Exit(api.ExitUsage, "no studio address for this configuration: `peaka config set studio URL`, or pass --sheet")
	}
	nonce, err := oauth.RandomState()
	if err != nil {
		return nil, err
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, api.Exit(api.ExitFailure, "cannot listen for the picks: %v", err)
	}
	defer lis.Close()
	port := lis.Addr().(*net.TCPAddr).Port
	page := fmt.Sprintf("%s/connections/pick/%s/%s/%d/%s", strings.TrimRight(r.Settings.Studio, "/"), url.PathEscape(project), url.PathEscape(connectionID), port, nonce)

	type answer struct {
		files     []pickedFile
		cancelled bool
	}
	picked := make(chan answer, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/picked" {
			http.NotFound(w, req)
			return
		}
		q := req.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if q.Get("nonce") != nonce {
			fmt.Fprint(w, callbackPage("Not for this terminal", "These picks were not asked for by this command."))
			return
		}
		var files []pickedFile
		if s := q.Get("sheets"); s != "" {
			_ = json.Unmarshal([]byte(s), &files)
		}
		if q.Get("cancelled") != "" || len(files) == 0 {
			fmt.Fprint(w, callbackPage("Nothing picked", "No spreadsheet was picked. You can close this window."))
		} else {
			fmt.Fprint(w, callbackPage("Picked", fmt.Sprintf("%d spreadsheet(s) go to the terminal. You can close this window.", len(files))))
		}
		select {
		case picked <- answer{files: files, cancelled: q.Get("cancelled") != "" || len(files) == 0}:
		default:
		}
	})}
	go srv.Serve(lis)
	defer srv.Shutdown(context.Background())

	fmt.Fprintf(r.Err, "Pick the spreadsheets for %s in your browser", connectionName)
	if err := openBrowser(page); err != nil {
		fmt.Fprintf(r.Err, ":\n  %s\n", page)
	} else {
		fmt.Fprintln(r.Err, " …")
	}
	fmt.Fprint(r.Err, "Waiting for the picks … ")
	wait, cancel := context.WithTimeout(r.Ctx, 10*time.Minute)
	defer cancel()
	select {
	case a := <-picked:
		if a.cancelled {
			fmt.Fprintln(r.Err, "cancelled")
			return nil, api.Exit(api.ExitUsage, "no spreadsheet picked")
		}
		fmt.Fprintf(r.Err, "✓ %d picked\n", len(a.files))
		return a.files, nil
	case <-wait.Done():
		fmt.Fprintln(r.Err)
		if r.Ctx.Err() != nil {
			return nil, api.Exit(api.ExitInterrupted, "interrupted")
		}
		return nil, api.Exit(api.ExitRetryable, "nothing picked in 10 minutes")
	}
}

// newConnectionsPickCmd lets a person pick a connection's files ahead of time;
// the result is what --sheet / extraParameters.sheets take.
func newConnectionsPickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pick ID|NAME",
		Short: "Pick the files a connection reads (Google Sheets) in the browser",
		Long: `Opens the studio's file picker in your browser for a connection whose data
lives in files the account chooses (Google Sheets), and prints the picks as the
catalog wants them: a list of {id, name}. 'peaka catalogs create --connection X'
runs the same picker itself on a terminal; a script passes --sheet ID|URL[=name]
instead, or -f extraParameters:='{"sheets": […]}'.`,
		Example: `  peaka connections pick sheets
  peaka connections pick sheets --format json > picks.json
  peaka catalogs create --name sheets --connection sheets --sheet 1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms=budget`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: argCompleter(completeConnections),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveConnection(rt, args[0], p); err != nil {
				return err
			}
			if !strings.EqualFold(p["connectionType"], "google_sheets") {
				return api.Exit(api.ExitInvalid, "connection %s is %s; only Google Sheets connections pick files", p["connectionName"], p["connectionType"])
			}
			if gf.noInput {
				return api.Exit(api.ExitUsage, "picking files needs a browser; pass --sheet ID|URL[=name] to `catalogs create` instead")
			}
			files, err := rt.pickFiles(p["connection"], p["connectionName"])
			if err != nil {
				return err
			}
			return rt.print(files)
		},
	}
}
