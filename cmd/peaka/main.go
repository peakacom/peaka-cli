package main

import (
	"os"

	"github.com/peakacom/peaka-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
