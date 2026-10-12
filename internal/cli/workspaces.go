package cli

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

func newWorkspacesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspaces",
		Aliases: []string{"workspace", "ws"},
		Short:   "Workspaces of the organization in context",
	}
	var name, description string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return api.Exit(api.ExitUsage, "--name is required")
			}
			orgID, err := rt.orgID()
			if err != nil {
				return err
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var out any
			body := map[string]any{"name": name}
			if description != "" {
				body["description"] = description
			}
			if _, err := client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf("/organizations/%s/workspaces", orgID), nil, body, &out); err != nil {
				return err
			}
			return rt.print(out)
		},
	}
	create.Flags().StringVar(&name, "name", "", "workspace name")
	create.Flags().StringVar(&description, "description", "", "description")

	var uName, uDescription string
	update := &cobra.Command{
		Use:   "update ID|NAME",
		Short: "Change a workspace's name or description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, err := rt.orgID()
			if err != nil {
				return err
			}
			ws, err := rt.resolveWorkspace(orgID, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"name": str(ws["name"]), "description": ws["description"]}
			if cmd.Flags().Changed("name") {
				body["name"] = uName
			}
			if cmd.Flags().Changed("description") {
				body["description"] = uDescription
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var out any
			if _, err := client.Do(rt.Ctx, http.MethodPut, fmt.Sprintf("/organizations/%s/workspaces/%s", orgID, str(ws["id"])), nil, body, &out); err != nil {
				return err
			}
			return rt.print(out)
		},
	}
	update.Flags().StringVar(&uName, "name", "", "new name")
	update.Flags().StringVar(&uDescription, "description", "", "new description")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List workspaces",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				orgID, err := rt.orgID()
				if err != nil {
					return err
				}
				wss, err := rt.listWorkspaces(orgID)
				if err != nil {
					return err
				}
				return rt.printWith(wss, "id", "name", "defaultWorkspace", "createdAt")
			},
		},
		&cobra.Command{
			Use:     "describe ID|NAME",
			Aliases: []string{"get"},
			Short:   "Show one workspace",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				orgID, err := rt.orgID()
				if err != nil {
					return err
				}
				ws, err := rt.resolveWorkspace(orgID, args[0])
				if err != nil {
					return err
				}
				client, err := rt.api()
				if err != nil {
					return err
				}
				var full json.RawMessage
				if err := client.Get(rt.Ctx, fmt.Sprintf("/organizations/%s/workspaces/%s", orgID, str(ws["id"])), nil, &full); err != nil {
					return err
				}
				var out any
				if err := decodeJSON(full, &out); err != nil {
					return err
				}
				return rt.print(out)
			},
		},
		create,
		update,
		&cobra.Command{
			Use:   "delete ID|NAME",
			Short: "Delete a workspace",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				orgID, err := rt.orgID()
				if err != nil {
					return err
				}
				ws, err := rt.resolveWorkspace(orgID, args[0])
				if err != nil {
					return err
				}
				if err := rt.confirm(fmt.Sprintf("Delete workspace %s (%s) and everything in it?", str(ws["name"]), str(ws["id"]))); err != nil {
					return err
				}
				client, err := rt.api()
				if err != nil {
					return err
				}
				if _, err := client.Do(rt.Ctx, http.MethodDelete, fmt.Sprintf("/organizations/%s/workspaces/%s", orgID, str(ws["id"])), nil, nil, nil); err != nil {
					return err
				}
				rt.note("Deleted workspace %s", str(ws["name"]))
				return nil
			},
		},
	)
	return cmd
}
