package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

func newConnectionsCmd() *cobra.Command {
	cmd := group("connections", "Connections: credentials to external systems; catalogs are built on them", []string{"connection", "conn"},
		opSpec{Use: "list", Short: "List connections", Method: http.MethodGet, Path: "/connections/{project}", Fields: []string{"id", "name", "type"}},
		opSpec{Use: "delete ID|NAME", Short: "Delete a connection", Method: http.MethodDelete, Path: "/connections/{project}/{connection}",
			Args: []argSpec{{Name: "ID", Resolve: resolveConnection}}, Confirm: "Delete connection {0}? Catalogs built on it stop working.", Empty: "Deleted."},
		opSpec{Use: "update ID|NAME", Short: "Change a connection's name or credential", Method: http.MethodPut, Path: "/connections/{project}/{connection}", Body: true,
			Long: `  peaka connections update warehouse --name warehouse-prod
  peaka connections update warehouse -c password=…            # credential fields, like create
  peaka connections update warehouse --from-file conn.json    # {name, type, credential}`,
			Args: []argSpec{{Name: "ID", Resolve: resolveConnection}},
			Flags: []flagSpec{
				{Name: "name", Help: "new name"},
				{Name: "credential", Short: "c", Kind: "strings", Help: "credential field KEY=VALUE; repeatable", Set: credentialSetter},
			},
			Prepare: func(c *callCtx) error {
				if _, ok := c.Body["type"]; !ok {
					c.Body["type"] = c.Params["connectionType"]
				}
				if _, ok := c.Body["name"]; !ok {
					c.Body["name"] = c.Params["connectionName"]
				}
				return nil
			}},
	)
	var detail bool
	describe := &cobra.Command{
		Use:               "describe ID|NAME",
		ValidArgsFunction: argCompleter(completeConnections),
		Aliases:           []string{"get"},
		Short:             "Show one connection; --detail adds the catalogs and status",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveConnection(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			path := fmt.Sprintf("/connections/%s/%s", project, p["connection"])
			if detail {
				path += "/detail"
			}
			return rt.call(http.MethodGet, path, nil, nil, "", nil, "")
		},
	}
	describe.Flags().BoolVar(&detail, "detail", false, "the detailed view")
	cmd.AddCommand(describe, newConnectionsCreateCmd(), newConnectionsWaitCmd(), newConnectionTypesCmd())
	return cmd
}

// credentialSetter folds -c KEY=VALUE (string) and KEY:=JSON (typed) into body.credential.
func credentialSetter(v any, body map[string]any) {
	cred, _ := body["credential"].(map[string]any)
	if cred == nil {
		cred = map[string]any{}
	}
	for _, kv := range v.([]string) {
		if k, val, ok := strings.Cut(kv, ":="); ok {
			var j any
			if json.Unmarshal([]byte(val), &j) == nil {
				cred[k] = j
				continue
			}
		}
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		cred[k] = val
	}
	body["credential"] = cred
}

// coerceCredential converts string values to the types the schema declares
// (number, boolean, object), so -c port=5432 -c useSsl=true just work.
func coerceCredential(cred map[string]any, cfg []any) {
	for _, cv := range cfg {
		c, _ := cv.(map[string]any)
		name := str(c["fieldName"])
		s, isString := cred[name].(string)
		if !isString {
			continue
		}
		switch strings.ToLower(str(c["fieldType"])) {
		case "number", "integer", "int", "port":
			var n json.Number = json.Number(strings.TrimSpace(s))
			if _, err := n.Int64(); err == nil {
				cred[name] = n
			} else if _, err := n.Float64(); err == nil {
				cred[name] = n
			}
		case "boolean", "bool", "checkbox":
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "yes", "1", "on":
				cred[name] = true
			case "false", "no", "0", "off":
				cred[name] = false
			}
		case "object", "json", "map":
			var j any
			if json.Unmarshal([]byte(s), &j) == nil {
				cred[name] = j
			}
		}
	}
}

// connectionTypes lists /connections/config (once per process).
func (r *Runtime) connectionTypes() ([]record, error) {
	if r.connTypes != nil {
		return r.connTypes, nil
	}
	ts, err := r.listRecords("/connections/config", nil, "")
	if err != nil {
		return nil, err
	}
	r.connTypes = ts
	return ts, nil
}

// connectionType finds a type by connectionType, label, name or keyword (case-insensitive).
func (r *Runtime) connectionType(ref string) (record, error) {
	ts, err := r.connectionTypes()
	if err != nil {
		return nil, err
	}
	want := normalizeName(ref)
	for _, t := range ts {
		if strings.EqualFold(str(t["connectionType"]), ref) {
			return t, nil
		}
	}
	// Loosely: GOOGLE_SHEETS is also "google-sheets", "Google Sheets" or "googlesheets";
	// the label and the name count too, then a keyword that names one type alone.
	var hits, byKeyword []record
	for _, t := range ts {
		if normalizeName(str(t["connectionType"])) == want || normalizeName(str(t["connectionTypeLabel"])) == want || normalizeName(str(t["name"])) == want {
			hits = append(hits, t)
			continue
		}
		if kws, ok := t["keywords"].([]any); ok {
			for _, k := range kws {
				if normalizeName(str(k)) == want {
					byKeyword = append(byKeyword, t)
					break
				}
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) == 0 && len(byKeyword) == 1 {
		return byKeyword[0], nil
	}
	close := closeConnectionTypes(ts, ref, 3)
	hint := "`peaka connections types list` shows them"
	if len(close) > 0 {
		hint = "did you mean " + strings.Join(close, ", ") + "?"
	}
	return nil, api.Exit(api.ExitNotFound, "unknown connection type %q; %s", ref, hint)
}

// closeConnectionTypes names the types that look like ref, by type, label,
// keyword, or a word of the type or label ("gogle" is a word away from Google).
func closeConnectionTypes(ts []record, ref string, n int) []string {
	byName := map[string][]string{} // any spelling or word → the types it names
	var candidates []string
	add := func(spelling, ct string) {
		if spelling == "" {
			return
		}
		if _, seen := byName[spelling]; !seen {
			candidates = append(candidates, spelling)
		}
		byName[spelling] = append(byName[spelling], ct)
	}
	for _, t := range ts {
		ct := str(t["connectionType"])
		add(ct, ct)
		add(str(t["connectionTypeLabel"]), ct)
		add(str(t["name"]), ct)
		if kws, ok := t["keywords"].([]any); ok {
			for _, k := range kws {
				add(str(k), ct)
			}
		}
		for _, w := range strings.FieldsFunc(ct+" "+str(t["connectionTypeLabel"]), func(c rune) bool {
			return c == '_' || c == '-' || c == ' ' || c == '/'
		}) {
			if len(w) >= 4 {
				add(strings.ToLower(w), ct)
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range suggestNames(candidates, ref, n*4) {
		group := append([]string(nil), byName[s]...)
		sort.Strings(group)
		for _, ct := range group {
			if !seen[ct] {
				seen[ct] = true
				out = append(out, ct)
			}
		}
		if len(out) >= n {
			break
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func newConnectionTypesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "types", Short: "Connection types and the credential each one needs"}
	var category, search string
	list := &cobra.Command{
		Use:   "list",
		Short: "List connection types",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ts, err := rt.connectionTypes()
			if err != nil {
				return err
			}
			var out []record
			q := strings.ToLower(search)
			for _, t := range ts {
				if category != "" && !strings.EqualFold(str(t["category"]), category) {
					continue
				}
				if q != "" {
					hay := strings.ToLower(str(t["connectionType"]) + " " + str(t["name"]) + " " + str(t["connectionTypeLabel"]) + " " + fmt.Sprint(t["keywords"]))
					if !strings.Contains(hay, q) {
						continue
					}
				}
				out = append(out, t)
			}
			sort.Slice(out, func(i, j int) bool { return str(out[i]["connectionType"]) < str(out[j]["connectionType"]) })
			return rt.printWith(out, "connectionType", "name", "category", "authorizationType")
		},
	}
	list.Flags().StringVar(&category, "category", "", "only this category (Database, CRM, …)")
	list.Flags().StringVar(&search, "search", "", "match type, name or keywords")
	describe := &cobra.Command{
		Use:               "describe TYPE",
		ValidArgsFunction: argCompleter(completeConnectionTypes),
		Aliases:           []string{"get"},
		Short:             "Show a connection type with the credential fields it needs",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := rt.connectionType(args[0])
			if err != nil {
				return err
			}
			if rt.Settings.Format != "table" {
				return rt.print(t)
			}
			fmt.Fprintf(rt.Out, "%s  (%s)\n", str(t["connectionType"]), str(t["name"]))
			fmt.Fprintf(rt.Out, "category: %s   authorization: %s   redirect: %v\n", str(t["category"]), str(t["authorizationType"]), t["redirectRequired"])
			if d := str(t["documentationUrl"]); d != "" {
				fmt.Fprintf(rt.Out, "docs: %s\n", d)
			}
			fmt.Fprintln(rt.Out)
			cfg, _ := t["configuration"].([]any)
			return rt.printWith(cfg, "fieldName", "fieldType", "required", "description")
		},
	}
	cmd.AddCommand(list, describe)
	return cmd
}

// --- create -----------------------------------------------------------------

type createConnFlags struct {
	typ, name      string
	credential     []string
	credentialFile string
	callbackPort   int
	noBrowser      bool
	wait           bool
	timeout        time.Duration
}

func newConnectionsCreateCmd() *cobra.Command {
	f := &createConnFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a connection; OAuth types open the browser and finish in the terminal",
		Long: `Credential fields come from -c KEY=VALUE, --credential-file, or, on a terminal, from
questions built from the type's schema (` + "`peaka connections types describe TYPE`" + `).

  peaka connections create --type postgres --name warehouse -c host=db.example.com -c port=5432 \
      -c database=app -c user=analyst                     # asks for the password
  peaka connections create --type hubspot --name crm      # browser opens; the CLI waits for the authorization
  peaka connections create --type hubspot --name crm --no-input
      # prints {"id","url","status":"PENDING_AUTHORIZATION"}; a person opens url, then: peaka connections wait ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runCreateConnection(f) },
	}
	cmd.Flags().StringVarP(&f.typ, "type", "t", "", "connection type (peaka connections types list)")
	_ = cmd.RegisterFlagCompletionFunc("type", flagCompleter(completeConnectionTypes))
	cmd.Flags().StringVarP(&f.name, "name", "n", "", "connection name")
	cmd.Flags().StringArrayVarP(&f.credential, "credential", "c", nil, "credential field KEY=VALUE; repeatable")
	cmd.Flags().StringVar(&f.credentialFile, "credential-file", "", "JSON file with the credential object (- for stdin)")
	cmd.Flags().IntVar(&f.callbackPort, "callback-port", 0, "local port for the OAuth redirect (default: a free one)")
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "print the authorization URL instead of opening it")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "with --no-input: wait for the authorization instead of returning at once")
	cmd.Flags().DurationVar(&f.timeout, "wait-timeout", 10*time.Minute, "how long to wait for the authorization")
	return cmd
}

func runCreateConnection(f *createConnFlags) error {
	project, err := rt.needProject()
	if err != nil {
		return err
	}
	var t record
	if f.typ == "" {
		if gf.noInput {
			return api.Exit(api.ExitInvalid, "--type is required")
		}
		if t, err = promptConnectionType(); err != nil {
			return err
		}
	} else if t, err = rt.connectionType(f.typ); err != nil {
		return err
	}
	typ := str(t["connectionType"])
	if f.name == "" {
		if gf.noInput {
			return api.Exit(api.ExitInvalid, "--name is required")
		}
		if f.name, err = prompt("Name", typ); err != nil {
			return err
		}
	}

	cred := map[string]any{}
	if f.credentialFile != "" {
		b, err := readInput(f.credentialFile)
		if err != nil {
			return usage("--credential-file: %v", err)
		}
		if err := decodeJSON(b, &cred); err != nil {
			return usage("--credential-file: not a JSON object: %v", err)
		}
	}
	body := map[string]any{"credential": cred}
	credentialSetter(f.credential, body)
	cred = body["credential"].(map[string]any)

	oauth := strings.EqualFold(str(t["authorizationType"]), "oauth2")
	redirect, _ := t["redirectRequired"].(bool)
	// Required fields: ask on a terminal, fail otherwise. OAuth types with a
	// redirect get their tokens from the provider, so nothing is asked.
	if !(oauth && redirect) {
		cfg, _ := t["configuration"].([]any)
		var missing []string
		for _, cv := range cfg {
			c, _ := cv.(map[string]any)
			name := str(c["fieldName"])
			req, _ := c["required"].(bool)
			if _, ok := cred[name]; ok || !req {
				continue
			}
			if gf.noInput {
				missing = append(missing, name)
				continue
			}
			label := name
			if d := str(c["description"]); d != "" && !strings.EqualFold(d, name) {
				label = fmt.Sprintf("%s (%s)", name, d)
			}
			var v string
			if isSecretField(name, str(c["fieldType"])) {
				v, err = readSecret(label + ": ")
			} else {
				v, err = prompt(label, "")
			}
			if err != nil {
				return err
			}
			if str(c["fieldType"]) == "object" || strings.HasPrefix(v, "{") {
				var j any
				if json.Unmarshal([]byte(v), &j) == nil {
					cred[name] = j
					continue
				}
			}
			cred[name] = v
		}
		if len(missing) > 0 {
			p := &api.Problem{Status: 400, Code: "VALIDATION_FAILED", Detail: fmt.Sprintf("connection type %s needs %s; pass them with -c KEY=VALUE", typ, strings.Join(missing, ", "))}
			for _, m := range missing {
				p.Errors = append(p.Errors, api.FieldError{Pointer: "/credential/" + m, Code: "REQUIRED", Message: "A value."})
			}
			return p
		}
	}
	if cfg, ok := t["configuration"].([]any); ok {
		coerceCredential(cred, cfg)
	}
	body["type"], body["name"] = typ, f.name

	client, err := rt.api()
	if err != nil {
		return err
	}
	if !(oauth && redirect) || hasOwnTokens(cred) {
		var out any
		if _, err := client.Do(rt.Ctx, http.MethodPost, "/connections/"+project, nil, body, &out); err != nil {
			return err
		}
		return rt.print(out)
	}

	// OAuth with a redirect: the provider sends the browser back to Peaka,
	// which completes the connection and redirects to connectionCallback.
	var lis net.Listener
	interactive := !gf.noInput
	if interactive {
		lis, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.callbackPort))
		if err != nil {
			return api.Exit(api.ExitFailure, "cannot listen for the OAuth redirect: %v", err)
		}
		defer lis.Close()
		body["connectionCallback"] = fmt.Sprintf("http://%s/callback", lis.Addr().String())
	}
	var started record
	if _, err := client.Do(rt.Ctx, http.MethodPost, "/connections/"+project, nil, body, &started); err != nil {
		return err
	}
	id, authURL := str(started["id"]), str(started["url"])
	if authURL == "" {
		return rt.print(started)
	}
	if !interactive {
		started["status"] = "PENDING_AUTHORIZATION"
		if !f.wait {
			rt.note("Open the url to authorize %s, then `peaka connections wait %s`.", typ, id)
			return rt.print(started)
		}
		rt.note("Authorize %s at:\n  %s\nWaiting for the authorization …", str(t["name"]), authURL)
		conn, err := rt.waitConnection(project, id, f.timeout)
		if err != nil {
			return err
		}
		return rt.print(conn)
	}

	fmt.Fprintf(rt.Err, "Authorize %s in your browser:\n  %s\n", str(t["name"]), authURL)
	if !f.noBrowser {
		if err := openBrowser(authURL); err != nil {
			fmt.Fprintf(rt.Err, "(could not open a browser: %v)\n", err)
		}
	}
	fmt.Fprint(rt.Err, "Waiting for the authorization … ")
	result := make(chan map[string]string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/callback" {
			http.NotFound(w, req)
			return
		}
		q := req.URL.Query()
		status := q.Get("status")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if status == "success" {
			fmt.Fprint(w, callbackPage("Connected", "The connection is ready. You can close this window and return to the terminal."))
		} else {
			fmt.Fprint(w, callbackPage("Authorization failed", "Peaka could not complete the connection. Return to the terminal for details."))
		}
		select {
		case result <- map[string]string{"status": status, "connectionId": q.Get("connectionId"), "error": q.Get("error")}:
		default:
		}
	})}
	go srv.Serve(lis)
	defer srv.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(rt.Ctx, f.timeout)
	defer cancel()
	select {
	case res := <-result:
		if res["status"] != "success" {
			fmt.Fprintln(rt.Err, "failed")
			return api.Exit(api.ExitFailed, "the provider did not authorize the connection%s", suffix(res["error"]))
		}
		fmt.Fprintln(rt.Err, "✓ connected")
	case <-ctx.Done():
		fmt.Fprintln(rt.Err)
		if rt.Ctx.Err() != nil {
			return api.Exit(api.ExitInterrupted, "interrupted; the authorization can still be finished in the browser, then `peaka connections wait %s`", id)
		}
		return api.Exit(api.ExitRetryable, "no authorization after %s; `peaka connections wait %s` keeps waiting", f.timeout, id)
	}
	var conn any
	if err := client.Get(rt.Ctx, fmt.Sprintf("/connections/%s/%s", project, id), nil, &conn); err != nil {
		return err
	}
	return rt.print(conn)
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

func hasOwnTokens(cred map[string]any) bool {
	for _, k := range []string{"accessToken", "refreshToken", "access_token", "refresh_token"} {
		if v, ok := cred[k]; ok && str(v) != "" {
			return true
		}
	}
	return false
}

func isSecretField(name, typ string) bool {
	n := strings.ToLower(name)
	return typ == "password" || strings.Contains(n, "password") || strings.Contains(n, "secret") || strings.Contains(n, "token") || strings.Contains(n, "apikey") || strings.Contains(n, "api_key") || strings.Contains(n, "privatekey")
}

// waitConnection polls until the connection exists (an OAuth connection is
// only stored once the provider has authorized it).
func (r *Runtime) waitConnection(project, id string, timeout time.Duration) (any, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		var conn any
		err := client.Get(r.Ctx, fmt.Sprintf("/connections/%s/%s", project, id), nil, &conn)
		if err == nil {
			return conn, nil
		}
		var p *api.Problem
		if !asProblem(err, &p) || p.Status != http.StatusNotFound {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, api.Exit(api.ExitRetryable, "connection %s is still waiting for its authorization after %s", id, timeout)
		}
		select {
		case <-time.After(2 * time.Second):
		case <-r.Ctx.Done():
			return nil, api.Exit(api.ExitInterrupted, "interrupted")
		}
	}
}

func newConnectionsWaitCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait ID",
		Short: "Wait until an OAuth connection has been authorized, then print it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			conn, err := rt.waitConnection(project, args[0], timeout)
			if err != nil {
				return err
			}
			return rt.print(conn)
		},
	}
	cmd.Flags().DurationVar(&timeout, "wait-timeout", 10*time.Minute, "how long to wait")
	return cmd
}

// prompt asks one question on stderr and reads a line from stdin.
func prompt(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(rt.Err, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(rt.Err, "%s: ", label)
	}
	var line string
	buf := make([]byte, 1)
	for {
		n, err := rt.In.Read(buf)
		if n == 0 || err != nil {
			break
		}
		if buf[0] == '\n' {
			break
		}
		line += string(buf[0])
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	return line, nil
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("no display")
		}
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func callbackPage(title, text string) string {
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>peaka · %s</title>
<style>body{font-family:-apple-system,Segoe UI,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#0b0e14;color:#e6e6e6}
.card{max-width:28rem;padding:2rem;border-radius:12px;background:#151a23;box-shadow:0 8px 32px rgba(0,0,0,.4)}h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:0;opacity:.8}</style></head>
<body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`, title, title, text)
}

// promptConnectionType asks for a connection type on a terminal and helps
// with the answer: any spelling of a type is taken, a close miss offers the
// nearest types as a numbered pick, "?" lists them all.
func promptConnectionType() (record, error) {
	ts, err := rt.connectionTypes()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 5; attempt++ {
		answer, err := prompt("Connection type (? lists them)", "")
		if err != nil {
			return nil, err
		}
		if answer == "?" || answer == "" {
			listConnectionTypes(ts, "")
			continue
		}
		t, err := rt.connectionType(answer)
		if err == nil {
			return t, nil
		}
		close := closeConnectionTypes(ts, answer, 6)
		if len(close) == 0 {
			// a word of the label, say "sheets" or "postgres": the types that mention it
			close = containingConnectionTypes(ts, answer, 6)
		}
		if len(close) == 0 {
			fmt.Fprintf(rt.Err, "No connection type looks like %q; ? lists them.\n", answer)
			continue
		}
		fmt.Fprintf(rt.Err, "No connection type is called %q. Did you mean:\n", answer)
		for i, ct := range close {
			fmt.Fprintf(rt.Err, "  %d) %-24s %s\n", i+1, ct, labelOf(ts, ct))
		}
		pick, err := prompt(fmt.Sprintf("Pick 1-%d, or type again", len(close)), "")
		if err != nil {
			return nil, err
		}
		if n, err := strconv.Atoi(pick); err == nil && n >= 1 && n <= len(close) {
			return rt.connectionType(close[n-1])
		}
		if pick != "" {
			if t, err := rt.connectionType(pick); err == nil {
				return t, nil
			}
		}
	}
	return nil, api.Exit(api.ExitUsage, "no connection type chosen; `peaka connections types list` shows them")
}

// containingConnectionTypes: the types whose type, label or keywords contain the word.
func containingConnectionTypes(ts []record, word string, n int) []string {
	want := normalizeName(word)
	if want == "" {
		return nil
	}
	var out []string
	for _, t := range ts {
		// the words of the type, its label and its keywords ("GOOGLE_SHEETS", "Google Sheets", "spreadsheet")
		words := strings.FieldsFunc(str(t["connectionType"])+" "+str(t["connectionTypeLabel"]), func(c rune) bool {
			return c == '_' || c == '-' || c == ' ' || c == '/'
		})
		if kws, ok := t["keywords"].([]any); ok {
			for _, k := range kws {
				words = append(words, str(k))
			}
		}
		for _, w := range words {
			if strings.HasPrefix(normalizeName(w), want) {
				out = append(out, str(t["connectionType"]))
				break
			}
		}
	}
	sort.Strings(out)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func labelOf(ts []record, connectionType string) string {
	for _, t := range ts {
		if str(t["connectionType"]) == connectionType {
			return firstNonEmpty(str(t["connectionTypeLabel"]), str(t["name"]))
		}
	}
	return ""
}

// listConnectionTypes prints the types, one per line, for a person choosing one.
func listConnectionTypes(ts []record, category string) {
	sorted := append([]record(nil), ts...)
	sort.Slice(sorted, func(i, j int) bool { return str(sorted[i]["connectionType"]) < str(sorted[j]["connectionType"]) })
	for _, t := range sorted {
		if category != "" && !strings.EqualFold(str(t["category"]), category) {
			continue
		}
		fmt.Fprintf(rt.Err, "  %-24s %-32s %s\n", str(t["connectionType"]), firstNonEmpty(str(t["connectionTypeLabel"]), str(t["name"])), str(t["category"]))
	}
}
