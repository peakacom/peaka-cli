package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in and manage stored identities",
	}
	cmd.AddCommand(newAuthLoginCmd(), newAuthLogoutCmd(), newAuthListCmd(), newAuthStatusCmd(), newAuthTokenCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var apiKey string
	var withKey bool
	var name string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an identity for the current api endpoint",
		Long: `Stores an identity for the API endpoint of the active configuration and makes it the configuration's account.

  peaka auth login --api-key            # prompts for the key (hidden)
  peaka auth login --api-key KEY        # key on the command line
  echo $KEY | peaka auth login --api-key --name ci

Browser login (peaka auth login without --api-key) arrives with the OAuth phase.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !withKey {
				return api.Exit(api.ExitUsage, "browser login is not available yet; use `peaka auth login --api-key` (or PEAKA_API_KEY)")
			}
			key := strings.TrimSpace(apiKey)
			if key == "" {
				var err error
				if key, err = readSecret("API key: "); err != nil {
					return err
				}
			}
			if key == "" {
				return api.Exit(api.ExitUsage, "empty API key")
			}
			client := rt.newClient(key)
			var info map[string]any
			if err := client.Get(rt.Ctx, "/info", nil, &info); err != nil {
				return err
			}
			acct := config.Account{
				Host:    rt.Settings.APIHost(),
				Kind:    "api-key",
				KeyType: str(info["keyType"]),
				KeyID:   str(info["keyId"]),
				Subject: firstNonEmpty(str(info["email"]), str(info["projectName"]), str(info["organizationName"])),
				Created: time.Now().UTC(),
			}
			acct.Name = name
			if acct.Name == "" {
				acct.Name = defaultAccountName(info)
			}
			if err := config.StoreAccount(acct, key); err != nil {
				return err
			}
			rt.Config.Account = acct.Name
			if err := config.Save(rt.Config); err != nil {
				return err
			}
			rt.note("✓ Stored %s for %s as account %q (configuration %s)", describeIdentity(info), acct.Host, acct.Name, rt.Config.Name)
			if rt.Settings.Format != "table" {
				return rt.print(map[string]any{"account": acct, "identity": info})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key; empty value prompts")
	cmd.Flags().Lookup("api-key").NoOptDefVal = " "
	cmd.Flags().StringVar(&name, "name", "", "name to store the account under (default: derived from the key)")
	cmd.PreRun = func(cmd *cobra.Command, args []string) { withKey = cmd.Flags().Changed("api-key") }
	return cmd
}

func newAuthLogoutCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout [ACCOUNT]",
		Short: "Forget a stored identity",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := rt.Settings.APIHost()
			if all {
				accts, err := config.Accounts(host)
				if err != nil {
					return err
				}
				for _, a := range accts {
					if err := config.RemoveAccount(host, a.Name); err != nil {
						return err
					}
				}
				rt.note("Removed %d account(s) for %s", len(accts), host)
			} else {
				name := rt.Settings.Account
				if len(args) == 1 {
					name = args[0]
				}
				if name == "" {
					return api.Exit(api.ExitUsage, "which account? `peaka auth list`, then `peaka auth logout NAME`")
				}
				if err := config.RemoveAccount(host, name); err != nil {
					return api.Exit(api.ExitNotFound, "%v", err)
				}
				rt.note("Removed account %q for %s", name, host)
			}
			if rt.Config.Account != "" {
				if a, _ := config.FindAccount(host, rt.Config.Account); a == nil {
					rt.Config.Account = ""
					_ = config.Save(rt.Config)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "remove every account stored for this api endpoint")
	return cmd
}

func newAuthListCmd() *cobra.Command {
	var allHosts bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored identities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host := rt.Settings.APIHost()
			if allHosts {
				host = ""
			}
			accts, err := config.Accounts(host)
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(accts))
			for _, a := range accts {
				rows = append(rows, map[string]any{
					"name": a.Name, "host": a.Host, "kind": a.Kind, "keyType": a.KeyType, "subject": a.Subject,
					"active":  a.Name == rt.Settings.Account && a.Host == rt.Settings.APIHost(),
					"created": a.Created.Format(time.RFC3339),
				})
			}
			return rt.printWith(rows, "name", "host", "kind", "keyType", "subject", "active")
		},
	}
	cmd.Flags().BoolVar(&allHosts, "all-hosts", false, "accounts for every api endpoint, not only the current one")
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which identity would be used, and whether it works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, acct, err := rt.token()
			if err != nil {
				return err
			}
			source := "account"
			name := ""
			if acct == nil {
				source = "PEAKA_API_KEY"
			} else {
				name = acct.Name
			}
			var info map[string]any
			client := rt.newClient(tok)
			infoErr := client.Get(rt.Ctx, "/info", nil, &info)
			out := map[string]any{
				"api": rt.Settings.API, "source": source, "account": name, "token": config.MaskSecret(tok),
				"valid": infoErr == nil, "identity": info,
			}
			if infoErr != nil {
				if p, ok := infoErr.(*api.Problem); ok {
					out["error"] = p
				} else {
					out["error"] = infoErr.Error()
				}
			}
			if rt.Settings.Format == "table" {
				fmt.Fprintf(rt.Out, "api      %s\nsource   %s\n", rt.Settings.API, source)
				if name != "" {
					fmt.Fprintf(rt.Out, "account  %s\n", name)
				}
				fmt.Fprintf(rt.Out, "token    %s\n", config.MaskSecret(tok))
				if infoErr != nil {
					fmt.Fprintf(rt.Out, "valid    no\n")
					return infoErr
				}
				fmt.Fprintf(rt.Out, "valid    yes — %s\n", describeIdentity(info))
				return nil
			}
			if err := rt.print(out); err != nil {
				return err
			}
			return infoErr
		},
	}
}

func newAuthTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-access-token",
		Short: "Print the credential the CLI would send (for curl and other tools)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, _, err := rt.token()
			if err != nil {
				return err
			}
			fmt.Fprintln(rt.Out, tok)
			return nil
		},
	}
}

// readSecret reads a line without echo on a terminal, or a plain line from a pipe.
func readSecret(prompt string) (string, error) {
	if rt.StdinTTY {
		fmt.Fprint(rt.Err, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(rt.Err)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := bufio.NewReader(rt.In).ReadString('\n')
	if err != nil && line == "" {
		return "", api.Exit(api.ExitUsage, "no API key on stdin")
	}
	return strings.TrimSpace(line), nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func defaultAccountName(info map[string]any) string {
	if e := str(info["email"]); e != "" {
		return e
	}
	kt := strings.ToLower(str(info["keyType"]))
	if kt == "" {
		kt = "key"
	}
	id := str(info["keyId"])
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		return kt
	}
	return kt + "-" + id
}

func describeIdentity(info map[string]any) string {
	if e := str(info["email"]); e != "" {
		return e
	}
	parts := []string{}
	if kt := str(info["keyType"]); kt != "" {
		parts = append(parts, kt+" key")
	}
	if p := str(info["projectName"]); p != "" {
		parts = append(parts, "project "+p)
	}
	if o := str(info["organizationName"]); o != "" {
		parts = append(parts, "org "+o)
	}
	if len(parts) == 0 {
		return "identity"
	}
	return strings.Join(parts, ", ")
}
