package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/config"
)

func newOrgsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "orgs",
		Aliases: []string{"organizations", "org"},
		Short:   "Organizations you belong to",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List organizations",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				orgs, err := rt.listOrgs()
				if err != nil {
					return err
				}
				for _, o := range orgs {
					o["current"] = str(o["id"]) == rt.Settings.Org
				}
				return rt.printWith(orgs, "id", "name", "owner", "createdAt", "current")
			},
		},
		&cobra.Command{
			Use:     "describe [ID|NAME]",
			Aliases: []string{"get"},
			Short:   "Show one organization (default: the one in context)",
			Args:    cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ref := rt.Settings.Org
				if len(args) == 1 {
					ref = args[0]
				}
				if ref == "" {
					return rt.needOrgErr()
				}
				org, err := rt.resolveOrg(ref)
				if err != nil {
					return err
				}
				client, err := rt.api()
				if err != nil {
					return err
				}
				var full json.RawMessage
				if err := client.Get(rt.Ctx, fmt.Sprintf("/organizations/%s", str(org["id"])), nil, &full); err != nil {
					return err
				}
				var out any
				if err := decodeJSON(full, &out); err != nil {
					return err
				}
				return rt.print(out)
			},
		},
		&cobra.Command{
			Use:   "use ID|NAME",
			Short: "Make an organization the context of the active configuration",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				org, err := rt.resolveOrg(args[0])
				if err != nil {
					return err
				}
				id := str(org["id"])
				if rt.Config.Org != id {
					rt.Config.Org = id
					rt.Config.Project = "" // a project belongs to one org
				}
				if err := config.Save(rt.Config); err != nil {
					return err
				}
				rt.note("Using organization %s (%s) in configuration %s", str(org["name"]), id, rt.Config.Name)
				return nil
			},
		},
	)
	return cmd
}

func (r *Runtime) needOrgErr() error {
	_, err := r.needOrg()
	return err
}
