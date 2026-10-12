package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Settings and named configurations",
		Long: `A configuration carries two addresses (api, dbc), an account and a context (org, project).
"environment" is a preset that fills api and dbc; either can be overridden.

Properties: ` + strings.Join(config.Properties, ", ") + `
Precedence: flag > environment variable > configuration > preset > default`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "set PROPERTY VALUE",
			Short: "Set a property in the active configuration",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := rt.Config.Set(args[0], args[1]); err != nil {
					return api.Exit(api.ExitUsage, "%v", err)
				}
				if err := config.Save(rt.Config); err != nil {
					return err
				}
				rt.note("Updated property [%s] in configuration %s", args[0], rt.Config.Name)
				return nil
			},
		},
		&cobra.Command{
			Use:   "get PROPERTY",
			Short: "Print a property's effective value",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				v, ok := rt.Settings.Values()[args[0]]
				if !ok {
					return api.Exit(api.ExitUsage, "unknown property %q: one of %s", args[0], strings.Join(config.Properties, ", "))
				}
				if rt.Settings.Format == "table" {
					fmt.Fprintln(rt.Out, v)
					return nil
				}
				return rt.print(map[string]any{"property": args[0], "value": v, "source": rt.Settings.Source[args[0]]})
			},
		},
		&cobra.Command{
			Use:   "unset PROPERTY",
			Short: "Remove a property from the active configuration",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := rt.Config.Set(args[0], ""); err != nil {
					return api.Exit(api.ExitUsage, "%v", err)
				}
				if err := config.Save(rt.Config); err != nil {
					return err
				}
				rt.note("Unset property [%s] in configuration %s", args[0], rt.Config.Name)
				return nil
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "Show every effective value and where it comes from",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				vals := rt.Settings.Values()
				rows := make([]map[string]any, 0, len(config.Properties))
				for _, p := range config.Properties {
					src := rt.Settings.Source[p]
					if vals[p] == "" {
						src = ""
					}
					rows = append(rows, map[string]any{"property": p, "value": vals[p], "source": src})
				}
				rt.note("configuration: %s", rt.Config.Name)
				return rt.printWith(rows, "property", "value", "source")
			},
		},
		newConfigurationsCmd(),
	)
	return cmd
}

func newConfigurationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "configurations",
		Aliases: []string{"configuration", "profiles"},
		Short:   "Create, list, activate and delete named configurations",
	}
	var activate bool
	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create an empty configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := config.Create(args[0]); err != nil {
				return api.Exit(api.ExitConflict, "%v", err)
			}
			rt.note("Created configuration %s", args[0])
			if activate {
				if err := config.SetActive(args[0]); err != nil {
					return err
				}
				rt.note("Activated configuration %s", args[0])
			}
			return nil
		},
	}
	create.Flags().BoolVar(&activate, "activate", false, "also make it the active configuration")
	cmd.AddCommand(
		create,
		&cobra.Command{
			Use:   "list",
			Short: "List configurations",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				names, err := config.List()
				if err != nil {
					return err
				}
				active := config.ActiveName()
				rows := make([]map[string]any, 0, len(names))
				for _, n := range names {
					c, err := config.Load(n)
					if err != nil {
						return err
					}
					s := config.Resolve(c, config.Overrides{})
					rows = append(rows, map[string]any{
						"name": n, "active": n == active, "environment": s.Environment, "api": s.API, "dbc": s.DBC,
						"account": c.Account, "org": c.Org, "project": c.Project,
					})
				}
				return rt.printWith(rows, "name", "active", "environment", "account", "org", "project")
			},
		},
		&cobra.Command{
			Use:   "activate NAME",
			Short: "Make a configuration the active one",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := config.SetActive(args[0]); err != nil {
					return api.Exit(api.ExitNotFound, "%v", err)
				}
				rt.note("Activated configuration %s", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use:   "describe [NAME]",
			Short: "Show a configuration's stored properties",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c := rt.Config
				if len(args) == 1 {
					var err error
					if c, err = config.Load(args[0]); err != nil {
						return api.Exit(api.ExitNotFound, "%v", err)
					}
				}
				out := map[string]any{"name": c.Name, "active": c.Name == config.ActiveName()}
				for _, p := range config.Properties {
					v, _ := c.Get(p)
					out[p] = v
				}
				return rt.print(out)
			},
		},
		&cobra.Command{
			Use:   "delete NAME",
			Short: "Delete a configuration",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := rt.confirm(fmt.Sprintf("Delete configuration %q?", args[0])); err != nil {
					return err
				}
				if err := config.Delete(args[0]); err != nil {
					return api.Exit(api.ExitConflict, "%v", err)
				}
				rt.note("Deleted configuration %s", args[0])
				return nil
			},
		},
	)
	return cmd
}
