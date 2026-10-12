package cli

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

// The skill: the short guide an AI agent loads to use this CLI well. It ships
// inside the binary so `peaka skill install` works wherever peaka does, and
// as docs/SKILL.md in the repository and the release archives.
//
//go:embed skill.md
var skillFile string

// skillTargets are the places agents read skills from, by agent.
var skillTargets = []struct {
	name, help string
	path       func(home, project string) string
}{
	{"claude", "Claude Code: ~/.claude/skills/peaka/SKILL.md (or .claude/skills/peaka/ in a project with --project-dir)",
		func(home, project string) string {
			if project != "" {
				return filepath.Join(project, ".claude", "skills", "peaka", "SKILL.md")
			}
			return filepath.Join(home, ".claude", "skills", "peaka", "SKILL.md")
		}},
	{"codex", "Codex CLI: ~/.codex/skills/peaka/SKILL.md",
		func(home, project string) string {
			if project != "" {
				return filepath.Join(project, ".codex", "skills", "peaka", "SKILL.md")
			}
			return filepath.Join(home, ".codex", "skills", "peaka", "SKILL.md")
		}},
	{"cursor", "Cursor: .cursor/rules/peaka.mdc in the project (--project-dir, default .)",
		func(home, project string) string {
			if project == "" {
				project = "."
			}
			return filepath.Join(project, ".cursor", "rules", "peaka.mdc")
		}},
}

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "The skill file for AI agents: show it or install it where agents look",
		Long: `An agent skill is a short Markdown guide (SKILL.md with a name and a
description) that an AI coding agent loads when a task matches it. peaka's
skill tells an agent how to find data, run SQL, create things and read errors
through this CLI. 'peaka skill show' prints it; 'peaka skill install' writes it
where an agent looks (Claude Code, Codex, Cursor), for the person or for one
project.`,
	}
	cmd.AddCommand(newSkillShowCmd(), newSkillInstallCmd())
	return cmd
}

func newSkillShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the skill file (SKILL.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A document, not a record: raw Markdown even in a pipe
			// (`peaka skill show > SKILL.md`), unless a format was asked for.
			if f := rt.Settings.Format; f != "table" && rt.Settings.Source["format"] != "default" {
				return rt.print(map[string]any{"name": "peaka", "file": "SKILL.md", "content": skillFile})
			}
			fmt.Fprint(rt.Out, skillFile)
			return nil
		},
	}
}

func newSkillInstallCmd() *cobra.Command {
	var agents []string
	var projectDir, dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the skill where AI agents look for it",
		Long: `Writes SKILL.md for the agents named by --agent (default: claude). With
--project-dir the skill goes into that project's own agent folder, so it is
shared through the repository; without it, into your home, for every project.
--dir writes the file into any directory instead.`,
		Example: `  peaka skill install                       # ~/.claude/skills/peaka/SKILL.md
  peaka skill install --agent claude,codex,cursor --project-dir .
  peaka skill install --dir ./agents/skills/peaka`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			var paths []string
			if dir != "" {
				paths = append(paths, filepath.Join(dir, "SKILL.md"))
			} else {
				for _, want := range agents {
					found := false
					for _, t := range skillTargets {
						if t.name == want {
							paths = append(paths, t.path(home, projectDir))
							found = true
						}
					}
					if !found {
						return api.Exit(api.ExitUsage, "unknown agent %q: claude, codex or cursor", want)
					}
				}
			}
			var written []string
			for _, p := range paths {
				content := skillFile
				if filepath.Ext(p) == ".mdc" {
					content = cursorRule(skillFile)
				}
				if gf.dryRun {
					fmt.Fprintf(rt.Err, "would write %s (%d bytes)\n", p, len(content))
					continue
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return api.Exit(api.ExitFailure, "%v", err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					return api.Exit(api.ExitFailure, "%v", err)
				}
				written = append(written, p)
				rt.note("✓ %s", p)
			}
			if rt.Settings.Format != "table" {
				return rt.print(map[string]any{"written": written})
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&agents, "agent", []string{"claude"}, "agents to install for: claude, codex, cursor (repeatable or comma-separated)")
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "install into this project's agent folder instead of your home")
	cmd.Flags().StringVar(&dir, "dir", "", "write SKILL.md into this directory instead")
	return cmd
}

// cursorRule wraps the skill as a Cursor rule: its front matter instead of the skill's.
func cursorRule(skill string) string {
	body := skill
	if len(body) > 4 && body[:4] == "---\n" {
		if end := indexAfter(body[4:], "\n---\n"); end > 0 {
			body = body[4+end+len("\n---\n"):]
		}
	}
	return "---\ndescription: Working with Peaka through the peaka CLI\nalwaysApply: false\n---\n" + body
}

func indexAfter(s, sep string) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
