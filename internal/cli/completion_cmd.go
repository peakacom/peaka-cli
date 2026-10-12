package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/release"
)

// The completion command: cobra's generators under the usual names
// (completion bash|zsh|fish|powershell, which the Homebrew cask installs
// from), plus `completion install`, which puts the one line a shell needs
// into its profile — once — so Tab works for people who installed from a
// zip, install.sh or Scoop.

const profileMarker = "# peaka: shell completion"

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Shell completion: print a script, or install it into your shell's profile",
		Long: `Tab completion of commands, flags and names (catalogs, tables, queries,
connections, projects…) for bash, zsh, fish and PowerShell.

'peaka completion install' finds your shell and adds the one line it needs to
its profile (or, for fish, the completion file it autoloads), once. A brew
install already has it. The scripts themselves: 'peaka completion zsh' and
friends.`,
	}
	gen := func(shell string, f func(w *bytes.Buffer) error) *cobra.Command {
		return &cobra.Command{
			Use: shell, Short: "Print the " + shell + " completion script", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				var b bytes.Buffer
				if err := f(&b); err != nil {
					return err
				}
				_, err := rt.Out.Write(b.Bytes())
				return err
			},
		}
	}
	cmd.AddCommand(
		gen("bash", func(w *bytes.Buffer) error { return root.GenBashCompletionV2(w, true) }),
		gen("zsh", func(w *bytes.Buffer) error { return root.GenZshCompletion(w) }),
		gen("fish", func(w *bytes.Buffer) error { return root.GenFishCompletion(w, true) }),
		gen("powershell", func(w *bytes.Buffer) error { return root.GenPowerShellCompletionWithDesc(w) }),
		newCompletionInstallCmd(root),
	)
	return cmd
}

func newCompletionInstallCmd(root *cobra.Command) *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Add completion to your shell's profile (bash, zsh, fish or PowerShell)",
		Long: `Adds to your shell's profile the line that loads peaka's completion, once:

  zsh         ~/.zshrc        source <(peaka completion zsh)
  bash        ~/.bashrc       source <(peaka completion bash)
  fish        ~/.config/fish/completions/peaka.fish   (the file itself; fish autoloads it)
  PowerShell  $PROFILE        peaka completion powershell | Out-String | Invoke-Expression

The shell is read from $SHELL (PowerShell on Windows); --shell picks one. Open a
new shell afterwards. A brew install needs none of this: the cask installs the
completions where bash, zsh and fish look.`,
		Example: `  peaka completion install
  peaka completion install --shell powershell`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shell == "" {
				shell = detectShell()
			}
			switch shell {
			case "zsh", "bash", "fish", "powershell", "pwsh":
			default:
				return api.Exit(api.ExitUsage, "which shell? --shell zsh|bash|fish|powershell")
			}
			if shell == "pwsh" {
				shell = "powershell"
			}
			if exe, _ := release.Executable(); release.Managed(exe) == "brew" && shell != "powershell" {
				rt.note("This peaka was installed by brew, which set up %s completion already; open a new shell.", shell)
				return nil
			}
			return installCompletion(root, shell)
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "", "bash, zsh, fish or powershell (default: your login shell)")
	return cmd
}

func detectShell() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	if os.Getenv("PSModulePath") != "" && os.Getenv("SHELL") == "" {
		return "powershell"
	}
	return filepath.Base(os.Getenv("SHELL"))
}

func installCompletion(root *cobra.Command, shell string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch shell {
	case "fish":
		path := filepath.Join(home, ".config", "fish", "completions", "peaka.fish")
		var b bytes.Buffer
		if err := root.GenFishCompletion(&b, true); err != nil {
			return err
		}
		if gf.dryRun {
			rt.note("would write %s", path)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			return err
		}
		rt.note("✓ %s (fish loads it on its own; open a new shell)", path)
		return nil
	case "zsh":
		// compdef needs compinit; load it only when the profile does not already
		line := "source <(peaka completion zsh)"
		path := filepath.Join(home, ".zshrc")
		if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "compinit") {
			line = "autoload -Uz compinit && compinit -i; " + line
		}
		return appendProfileLine(path, line)
	case "bash":
		path := filepath.Join(home, ".bashrc")
		if runtime.GOOS == "darwin" {
			path = filepath.Join(home, ".bash_profile") // what macOS's Terminal reads
		}
		return appendProfileLine(path, "source <(peaka completion bash)")
	case "powershell":
		path := powershellProfile(home)
		return appendProfileLine(path, "peaka completion powershell | Out-String | Invoke-Expression")
	}
	return nil
}

// appendProfileLine adds the marker and the line to a profile, unless they are there.
func appendProfileLine(path, line string) error {
	existing, _ := os.ReadFile(path)
	if strings.Contains(string(existing), profileMarker) || strings.Contains(string(existing), line) {
		rt.note("%s already loads peaka's completion; open a new shell if Tab does nothing yet.", path)
		return nil
	}
	if gf.dryRun {
		rt.note("would append to %s:\n  %s\n  %s", path, profileMarker, line)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return api.Exit(api.ExitFailure, "cannot write %s: %v", path, err)
	}
	defer f.Close()
	sep := "\n"
	if len(existing) == 0 || bytes.HasSuffix(existing, []byte("\n")) {
		sep = ""
	}
	if _, err := fmt.Fprintf(f, "%s\n%s\n%s\n", sep, profileMarker, line); err != nil {
		return err
	}
	rt.note("✓ added to %s:\n  %s\nOpen a new shell (or run the line) and Tab away.", path, line)
	return nil
}

// powershellProfile asks PowerShell where its profile is; without one on the
// PATH it uses the documented default of PowerShell 7, then 5.1.
func powershellProfile(home string) string {
	for _, sh := range []string{"pwsh", "powershell"} {
		out, err := exec.Command(sh, "-NoProfile", "-NonInteractive", "-Command", "$PROFILE").Output()
		if err == nil {
			if p := strings.TrimSpace(string(out)); p != "" {
				return p
			}
		}
	}
	if runtime.GOOS == "windows" {
		if p := filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"); fileExists(filepath.Dir(p)) {
			return p
		}
		return filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1")
	}
	return filepath.Join(home, ".config", "powershell", "Microsoft.PowerShell_profile.ps1")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
