package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/build"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.Settings.Format == "table" {
				fmt.Fprintf(rt.Out, "peaka %s (%s) %s/%s\n", build.Version, build.Commit, runtime.GOOS, runtime.GOARCH)
				return nil
			}
			return rt.print(map[string]any{
				"version": build.Version, "commit": build.Commit, "go": runtime.Version(),
				"os": runtime.GOOS, "arch": runtime.GOARCH,
			})
		},
	}
}
