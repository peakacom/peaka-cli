package cli

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
)

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "Projects of the organization in context",
	}
	var workspace string
	list := &cobra.Command{
		Use:   "list",
		Short: "List projects across the organization's workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			orgID, err := rt.orgID()
			if err != nil {
				return err
			}
			ps, err := rt.listProjects(orgID, workspace)
			if err != nil {
				return err
			}
			for _, p := range ps {
				p["current"] = str(p["id"]) == rt.Settings.Project
			}
			return rt.printWith(ps, "id", "name", "workspace", "owner", "createdAt", "current")
		},
	}
	list.Flags().StringVar(&workspace, "workspace", "", "only this workspace (id or name)")

	var cName, cDescription, cWorkspace string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cName == "" {
				return api.Exit(api.ExitUsage, "--name is required")
			}
			orgID, err := rt.orgID()
			if err != nil {
				return err
			}
			wss, err := rt.listWorkspaces(orgID)
			if err != nil {
				return err
			}
			var ws record
			if cWorkspace != "" {
				if ws, err = matchOne(wss, cWorkspace, "workspace"); err != nil {
					return err
				}
			} else {
				for _, w := range wss {
					if b, _ := w["defaultWorkspace"].(bool); b {
						ws = w
					}
				}
				if ws == nil && len(wss) == 1 {
					ws = wss[0]
				}
				if ws == nil {
					return api.Exit(api.ExitUsage, "no default workspace; pass --workspace")
				}
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			body := map[string]any{"name": cName}
			if cDescription != "" {
				body["description"] = cDescription
			}
			var out any
			if _, err := client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf("/organizations/%s/workspaces/%s/projects", orgID, str(ws["id"])), nil, body, &out); err != nil {
				return err
			}
			return rt.print(out)
		},
	}
	create.Flags().StringVar(&cName, "name", "", "project name")
	create.Flags().StringVar(&cDescription, "description", "", "description")
	create.Flags().StringVar(&cWorkspace, "workspace", "", "workspace id or name (default: the organization's default workspace)")

	var uName, uDescription string
	update := &cobra.Command{
		Use:   "update [ID|NAME]",
		Short: "Change a project's name or description (default: the project in context)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, orgID, err := rt.projectArg(args)
			if err != nil {
				return err
			}
			body := map[string]any{"name": str(p["name"]), "description": p["description"]}
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
			if _, err := client.Do(rt.Ctx, http.MethodPut, projectPath(orgID, p), nil, body, &out); err != nil {
				return err
			}
			return rt.print(out)
		},
	}
	update.Flags().StringVar(&uName, "name", "", "new name")
	update.Flags().StringVar(&uDescription, "description", "", "new description")

	cmd.AddCommand(
		list,
		&cobra.Command{
			Use:     "describe [ID|NAME]",
			Aliases: []string{"get"},
			Short:   "Show one project (default: the project in context)",
			Args:    cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, orgID, err := rt.projectArg(args)
				if err != nil {
					return err
				}
				client, err := rt.api()
				if err != nil {
					return err
				}
				var full json.RawMessage
				if err := client.Get(rt.Ctx, projectPath(orgID, p), nil, &full); err != nil {
					return err
				}
				var out record
				if err := decodeJSON(full, &out); err != nil {
					return err
				}
				out["workspace"] = p["workspace"]
				return rt.print(out)
			},
		},
		create,
		update,
		&cobra.Command{
			Use:   "delete ID|NAME",
			Short: "Delete a project",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, orgID, err := rt.projectArg(args)
				if err != nil {
					return err
				}
				if err := rt.confirm(fmt.Sprintf("Delete project %s (%s) with its connections, catalogs and queries?", str(p["name"]), str(p["id"]))); err != nil {
					return err
				}
				client, err := rt.api()
				if err != nil {
					return err
				}
				if _, err := client.Do(rt.Ctx, http.MethodDelete, projectPath(orgID, p), nil, nil, nil); err != nil {
					return err
				}
				if rt.Config.Project == str(p["id"]) {
					rt.Config.Project = ""
					_ = config.Save(rt.Config)
				}
				rt.note("Deleted project %s", str(p["name"]))
				return nil
			},
		},
		&cobra.Command{
			Use:   "use ID|NAME",
			Short: "Make a project the context of the active configuration",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, _, err := rt.projectArg(args)
				if err != nil {
					return err
				}
				rt.Config.Project = str(p["id"])
				if err := config.Save(rt.Config); err != nil {
					return err
				}
				rt.note("Using project %s (%s) in configuration %s", str(p["name"]), str(p["id"]), rt.Config.Name)
				return nil
			},
		},
	)
	return cmd
}

// projectArg resolves args[0] (or the project in context) to a project record and its org id.
func (r *Runtime) projectArg(args []string) (record, string, error) {
	ref := r.Settings.Project
	if len(args) == 1 {
		ref = args[0]
	}
	if ref == "" {
		_, err := r.needProject()
		return nil, "", err
	}
	orgID, err := r.orgID()
	if err != nil {
		return nil, "", err
	}
	ps, err := r.listProjects(orgID, "")
	if err != nil {
		return nil, "", err
	}
	p, err := matchOne(ps, ref, "project")
	return p, orgID, err
}

func projectPath(orgID string, p record) string {
	return fmt.Sprintf("/organizations/%s/workspaces/%s/projects/%s", orgID, str(p["workspaceId"]), str(p["id"]))
}
