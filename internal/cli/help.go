package cli

import (
	_ "embed"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

//go:embed agent-guide.md
var agentGuide string

// newHelpCmd replaces cobra's help with one that can also print the agent
// guide and the whole command tree as JSON.
func newHelpCmd(root *cobra.Command) *cobra.Command {
	var agent bool
	cmd := &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command; --agent prints the guide for AI agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if agent {
				fmt.Fprint(rt.Out, agentGuide)
				return nil
			}
			target := root
			if len(args) > 0 {
				c, _, err := root.Find(args)
				if err != nil || c == nil {
					return fmt.Errorf("unknown command %q", args)
				}
				target = c
			}
			if rt.Settings.Format == "json" || rt.Settings.Format == "yaml" {
				return rt.print(describeCommand(target))
			}
			return target.Help()
		},
	}
	cmd.Flags().BoolVar(&agent, "agent", false, "print the short guide for AI agents (stable, ~2 KB)")
	return cmd
}

func describeCommand(c *cobra.Command) map[string]any {
	m := map[string]any{
		"name":  c.Name(),
		"use":   c.UseLine(),
		"short": c.Short,
	}
	if c.Long != "" {
		m["long"] = c.Long
	}
	if len(c.Aliases) > 0 {
		m["aliases"] = c.Aliases
	}
	var flags []map[string]any
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		flags = append(flags, map[string]any{
			"name": f.Name, "shorthand": f.Shorthand, "type": f.Value.Type(), "default": f.DefValue, "usage": f.Usage,
		})
	})
	if len(flags) > 0 {
		m["flags"] = flags
	}
	var subs []map[string]any
	for _, s := range c.Commands() {
		if s.Hidden || s.Name() == "help" || s.Name() == "completion" {
			continue
		}
		subs = append(subs, describeCommand(s))
	}
	if len(subs) > 0 {
		m["commands"] = subs
	}
	return m
}
