package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

func newAPICmd() *cobra.Command {
	var fields []string
	var input string
	var raw, include bool
	cmd := &cobra.Command{
		Use:   "api METHOD PATH",
		Short: "Call any endpoint of the REST API with the current identity",
		Long: `Sends one request to the REST API (paths are relative to the api base, e.g. /organizations).
Non-2xx answers are reported like every other error: problem+json on stderr, exit code by code.

  peaka api GET /organizations
  peaka api GET /connections/config -f category=database
  peaka api POST /data/projects/$P/queries/execute -f statement='SELECT 1' --jq .data
  peaka api PUT /organizations/$O/workspaces/$W --input body.json
  cat body.json | peaka api POST /connections/$P --input -

-f KEY=VALUE adds a string; -f KEY:=JSON adds raw JSON (numbers, booleans, objects).
For GET and DELETE the fields become query parameters; otherwise they form the JSON body.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			query := url.Values{}
			body := map[string]any{}
			for _, f := range fields {
				k, v, isJSON, err := parseField(f)
				if err != nil {
					return err
				}
				if method == http.MethodGet || method == http.MethodDelete {
					query.Add(k, fmt.Sprint(v))
					continue
				}
				if isJSON {
					body[k] = v
				} else {
					body[k] = v
				}
			}
			var payload any
			if input != "" {
				var b []byte
				if input == "-" {
					b, err = io.ReadAll(rt.In)
				} else {
					b, err = os.ReadFile(input)
				}
				if err != nil {
					return api.Exit(api.ExitUsage, "--input: %v", err)
				}
				payload = b
			} else if len(body) > 0 {
				payload = body
			}
			var out []byte
			resp, err := client.Do(rt.Ctx, method, path, query, payload, &out)
			if include && resp != nil {
				fmt.Fprintf(rt.Err, "HTTP %d\n", resp.Status)
				for k, vs := range resp.Header {
					for _, v := range vs {
						fmt.Fprintf(rt.Err, "%s: %s\n", k, v)
					}
				}
				fmt.Fprintln(rt.Err)
			}
			if err != nil {
				return err
			}
			if len(out) == 0 {
				if rt.StdoutTTY {
					rt.note("HTTP %d, empty body", resp.Status)
				}
				return nil
			}
			if raw || !json.Valid(out) {
				_, err := rt.Out.Write(out)
				if err == nil && !strings.HasSuffix(string(out), "\n") {
					fmt.Fprintln(rt.Out)
				}
				return err
			}
			var v any
			dec := json.NewDecoder(strings.NewReader(string(out)))
			dec.UseNumber()
			if err := dec.Decode(&v); err != nil {
				return err
			}
			return rt.print(v)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "KEY=VALUE string field, or KEY:=JSON raw field; repeatable")
	cmd.Flags().StringVar(&input, "input", "", "file with the request body, or - for stdin")
	cmd.Flags().BoolVar(&raw, "raw", false, "write the response body as received")
	cmd.Flags().BoolVarP(&include, "include", "i", false, "print status and headers to stderr")
	return cmd
}

func parseField(f string) (key string, value any, isJSON bool, err error) {
	if i := strings.Index(f, ":="); i > 0 {
		var v any
		if err := json.Unmarshal([]byte(f[i+2:]), &v); err != nil {
			return "", nil, false, api.Exit(api.ExitUsage, "-f %s: value is not JSON: %v", f[:i], err)
		}
		return f[:i], v, true, nil
	}
	if i := strings.Index(f, "="); i > 0 {
		return f[:i], f[i+1:], false, nil
	}
	return "", nil, false, api.Exit(api.ExitUsage, "-f %q: expected KEY=VALUE or KEY:=JSON", f)
}
