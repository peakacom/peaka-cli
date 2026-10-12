package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

// Connectors that read files the account picks (Google Sheets today): a
// catalog needs the picked files in extraParameters.sheets. The studio opens
// Google's file picker for that; the CLI does the same from a page it serves
// on the loopback address, with the credentials the API hands out, or takes
// the spreadsheets as --sheet flags (an id or a Google Sheets URL, with an
// optional =name) for scripts and agents.

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
			// reported again, with the flag named, when the request is prepared
			continue
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

// pickerCredential is what GET /connections/{p}/{c}/picker answers.
type pickerCredential struct {
	Provider    string `json:"provider"`
	AccessToken string `json:"accessToken"`
	ClientID    string `json:"clientId"`
	APIKey      string `json:"apiKey"`
	Picks       string `json:"picks"`
}

// pickFiles opens Google's file picker in the browser from a page served on
// the loopback address and returns what the person picked.
func (r *Runtime) pickFiles(connectionID, connectionName string) ([]pickedFile, error) {
	project, err := r.needProject()
	if err != nil {
		return nil, err
	}
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	var cred pickerCredential
	if err := client.Get(r.Ctx, fmt.Sprintf("/connections/%s/%s/picker", project, url.PathEscape(connectionID)), nil, &cred); err != nil {
		return nil, err
	}
	if cred.AccessToken == "" || cred.APIKey == "" {
		return nil, api.Exit(api.ExitFailure, "the API gave no picker credentials for connection %s", connectionName)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, api.Exit(api.ExitFailure, "cannot listen for the picker: %v", err)
	}
	defer lis.Close()
	origin := "http://" + lis.Addr().String()
	picked := make(chan []pickedFile, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pickerPage.Execute(w, map[string]string{"Token": cred.AccessToken, "APIKey": cred.APIKey, "ClientID": cred.ClientID, "Connection": connectionName, "Origin": origin})
	})
	mux.HandleFunc("/picked", func(w http.ResponseWriter, req *http.Request) {
		var files []pickedFile
		if err := json.NewDecoder(req.Body).Decode(&files); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		select {
		case picked <- files:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(lis)
	defer srv.Shutdown(context.Background())

	fmt.Fprintf(r.Err, "Pick the spreadsheets for %s in your browser", connectionName)
	if err := openBrowser(origin); err != nil {
		fmt.Fprintf(r.Err, ":\n  %s\n", origin)
	} else {
		fmt.Fprintln(r.Err, " …")
	}
	fmt.Fprint(r.Err, "Waiting for the picks … ")
	wait, cancel := context.WithTimeout(r.Ctx, 10*time.Minute)
	defer cancel()
	select {
	case files := <-picked:
		if len(files) == 0 {
			fmt.Fprintln(r.Err, "cancelled")
			return nil, api.Exit(api.ExitUsage, "no spreadsheet picked")
		}
		fmt.Fprintf(r.Err, "✓ %d picked\n", len(files))
		return files, nil
	case <-wait.Done():
		fmt.Fprintln(r.Err)
		if r.Ctx.Err() != nil {
			return nil, api.Exit(api.ExitInterrupted, "interrupted")
		}
		return nil, api.Exit(api.ExitRetryable, "nothing picked in 10 minutes")
	}
}

// pickerPage runs Google's picker the way the studio does (spreadsheets view,
// shared drives, several files) and posts the picks back to the CLI.
var pickerPage = template.Must(template.New("picker").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Peaka · pick spreadsheets</title>
<style>body{font-family:-apple-system,Segoe UI,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem;color:#222}
h1{font-size:1.3rem}button{font-size:1rem;padding:.6rem 1.2rem;border-radius:.4rem;border:1px solid #888;background:#fff;cursor:pointer}
p.muted{color:#666}</style></head>
<body><h1>Pick the spreadsheets for connection {{.Connection}}</h1>
<p class="muted">Google's file picker opens here. Pick one or more spreadsheets; the terminal continues when you are done.</p>
<button id="open">Open the picker again</button>
<p id="status" class="muted"></p>
<script src="https://apis.google.com/js/api.js"></script>
<script>
const token = {{.Token}}, apiKey = {{.APIKey}}, origin = {{.Origin}};
const status = document.getElementById('status');
function send(files) {
  fetch('/picked', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(files)})
    .then(() => { status.textContent = files.length ? 'Picked ' + files.length + ' spreadsheet(s). You can close this window.' : 'Cancelled. You can close this window.'; });
}
function openPicker() {
  const view = new google.picker.DocsView(google.picker.ViewId.SPREADSHEETS).setIncludeFolders(true).setSelectFolderEnabled(false);
  const picker = new google.picker.PickerBuilder()
    .setOAuthToken(token).setDeveloperKey(apiKey).setOrigin(origin)
    .enableFeature(google.picker.Feature.MULTISELECT_ENABLED).enableFeature(google.picker.Feature.SUPPORT_DRIVES)
    .addView(view)
    .setCallback((data) => {
      if (data.action === google.picker.Action.PICKED) {
        send(data.docs.filter(d => d.mimeType === 'application/vnd.google-apps.spreadsheet').map(d => ({id: d.id, name: d.name})));
      } else if (data.action === google.picker.Action.CANCEL) {
        send([]);
      }
    }).build();
  picker.setVisible(true);
}
gapi.load('picker', {callback: openPicker, onerror: () => { status.textContent = 'Google\'s picker could not be loaded.'; }});
document.getElementById('open').onclick = openPicker;
</script></body></html>`))

// newConnectionsPickCmd lets a person pick a connection's files ahead of time;
// the result is what --sheet / extraParameters.sheets take.
func newConnectionsPickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pick ID|NAME",
		Short: "Pick the files a connection reads (Google Sheets) in the browser",
		Long: `Opens the provider's file picker in your browser for a connection whose data
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
