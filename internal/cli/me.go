package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newMeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Who am I, and what is the current context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := rt.api()
			if err != nil {
				return err
			}
			var info map[string]any
			if err := client.Get(rt.Ctx, "/info", nil, &info); err != nil {
				return err
			}
			ctx := map[string]any{
				"configuration": rt.Config.Name,
				"environment":   rt.Settings.Environment,
				"api":           rt.Settings.API,
				"dbc":           rt.Settings.DBC,
				"account":       rt.Settings.Account,
				"org":           rt.Settings.Org,
				"project":       rt.Settings.Project,
			}
			if rt.Settings.Format != "table" {
				return rt.print(map[string]any{"identity": info, "context": ctx})
			}
			fmt.Fprintf(rt.Out, "identity   %s\n", describeIdentity(info))
			for _, k := range []string{"keyType", "keyId", "organizationName", "organizationId", "projectName", "projectId", "email", "userId"} {
				if v := str(info[k]); v != "" {
					fmt.Fprintf(rt.Out, "  %-16s %s\n", k, v)
				}
			}
			fmt.Fprintf(rt.Out, "context    configuration %s\n", rt.Config.Name)
			for _, k := range []string{"environment", "api", "dbc", "account", "org", "project"} {
				v := str(ctx[k])
				if v == "" {
					v = "—"
				}
				fmt.Fprintf(rt.Out, "  %-16s %s\n", k, v)
			}
			return nil
		},
	}
}
