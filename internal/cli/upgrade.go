package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/build"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/release"
)

// peaka upgrade: replace this executable with the latest release (or a named
// one) from GitHub, after checking the published checksum. Installs owned by
// a package manager are pointed at it instead.

func newUpgradeCmd() *cobra.Command {
	var check bool
	var version string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the CLI to the latest release",
		Long: `Downloads the latest release of peaka from github.com/` + release.Repo + `, verifies
its checksum and replaces this executable. With --check it only says whether a
newer release exists (exit 0 when up to date, 1 when not). A brew-installed
peaka is upgraded with 'brew upgrade peaka' instead.`,
		Example: `  peaka upgrade
  peaka upgrade --check
  peaka upgrade --version v0.3.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(cmd.Context(), check, version)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().StringVar(&version, "version", "", "install this release (v1.2.3) instead of the latest")
	return cmd
}

func runUpgrade(ctx context.Context, check bool, version string) error {
	gh := release.New()
	var rel *release.Release
	var err error
	if version != "" {
		rel, err = gh.ByTag(ctx, version)
	} else {
		rel, err = gh.Latest(ctx)
	}
	if err != nil {
		return api.Exit(api.ExitRetryable, "%v", err)
	}
	current := build.Version
	newer := release.Compare(current, rel.Version) < 0
	exe, _ := release.Executable()
	manager := release.Managed(exe)
	rememberLatest(rel.Version)

	if rt.Settings.Format != "table" {
		if err := rt.print(map[string]any{"current": current, "latest": rel.Version, "newer": newer, "release": rel.URL,
			"executable": exe, "manager": manager, "checked": check}); err != nil {
			return err
		}
		if check {
			if newer {
				return api.Exit(api.ExitFailure, "peaka %s is available; you have %s", rel.Version, current)
			}
			return nil
		}
	}
	if check {
		if newer {
			rt.note("peaka %s is available (you have %s): %s", rel.Version, current, upgradeHint(manager))
			return api.Exit(api.ExitFailure, "a newer release exists")
		}
		rt.note("peaka %s is up to date", current)
		return nil
	}
	if version == "" && !newer {
		rt.note("peaka %s is up to date (latest release %s)", current, rel.Version)
		return nil
	}
	if manager != "" {
		return api.Exit(api.ExitUsage, "this peaka is installed by %s: %s", manager, upgradeHint(manager))
	}
	if !release.IsRelease(current) {
		rt.note("note: this is a development build (%s); it is replaced by release %s", current, rel.Version)
	}
	name := release.ArchiveName(rel.Version, runtime.GOOS, runtime.GOARCH)
	asset, ok := rel.Assets[name]
	if !ok {
		return api.Exit(api.ExitFailure, "release %s has no build for %s/%s (%s)", rel.Tag, runtime.GOOS, runtime.GOARCH, rel.URL)
	}
	sums, ok := rel.Assets["checksums.txt"]
	if !ok {
		return api.Exit(api.ExitFailure, "release %s publishes no checksums.txt; not installing", rel.Tag)
	}
	if err := rt.confirm(fmt.Sprintf("Replace %s (%s) with peaka %s?", exe, current, rel.Version)); err != nil {
		return err
	}
	rt.note("Downloading %s (%.1f MB) …", name, float64(asset.Size)/1e6)
	archive, err := gh.Download(ctx, asset)
	if err != nil {
		return api.Exit(api.ExitRetryable, "%v", err)
	}
	sumData, err := gh.Download(ctx, sums)
	if err != nil {
		return api.Exit(api.ExitRetryable, "%v", err)
	}
	if err := release.Verify(name, archive, release.Checksums(sumData)); err != nil {
		return api.Exit(api.ExitFailure, "%v", err)
	}
	bin, err := release.Binary(name, archive)
	if err != nil {
		return api.Exit(api.ExitFailure, "%v", err)
	}
	if err := release.Install(exe, bin); err != nil {
		return api.Exit(api.ExitFailure, "%v (try again with sudo, or install with brew)", err)
	}
	rt.note("✓ peaka %s installed at %s", rel.Version, exe)
	return nil
}

func upgradeHint(manager string) string {
	switch manager {
	case "brew":
		return "brew upgrade peaka"
	case "package":
		return "upgrade the peaka package with your package manager"
	}
	return "peaka upgrade"
}

// --- the once-a-day hint on a terminal ----------------------------------------

type updateCheck struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
}

func updateCheckPath() string { return filepath.Join(config.Dir(), "update-check.json") }

func readUpdateCheck() updateCheck {
	var u updateCheck
	if b, err := os.ReadFile(updateCheckPath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	return u
}

func rememberLatest(latest string) {
	b, _ := json.Marshal(updateCheck{CheckedAt: time.Now().UTC(), Latest: latest})
	_ = os.MkdirAll(config.Dir(), 0o700)
	_ = os.WriteFile(updateCheckPath(), b, 0o600)
}

// notifyUpdate tells a person at a terminal, at most once a day, that a newer
// release exists. Release builds only; PEAKA_NO_UPDATE_CHECK=1 turns it off.
func notifyUpdate(cmdName string) {
	if rt == nil || !rt.StdoutTTY || !api.IsTerminal(os.Stderr) || gf.quiet || gf.noInput {
		return
	}
	if os.Getenv("PEAKA_NO_UPDATE_CHECK") != "" || !release.IsRelease(build.Version) {
		return
	}
	switch cmdName {
	case "upgrade", "version", "mcp", "completion", "help", "":
		return
	}
	last := readUpdateCheck()
	if time.Since(last.CheckedAt) < 24*time.Hour {
		if last.Latest != "" && release.Compare(build.Version, last.Latest) < 0 {
			hintUpdate(last.Latest)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rel, err := release.New().Latest(ctx)
	if err != nil {
		rememberLatest(last.Latest) // try again tomorrow, not on every command
		return
	}
	rememberLatest(rel.Version)
	if release.Compare(build.Version, rel.Version) < 0 {
		hintUpdate(rel.Version)
	}
}

func hintUpdate(latest string) {
	exe, _ := release.Executable()
	fmt.Fprintf(os.Stderr, "\npeaka %s is available (you have %s): %s\n", latest, build.Version, upgradeHint(release.Managed(exe)))
}
