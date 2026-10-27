// Every build of brickie runs in FIPS 140-3 mode; release builds also pin the
// certified Go Cryptographic Module (see .goreleaser.yml).
//
//go:debug fips140=on
package main

import (
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "brickie",
		Short: "Agent sandboxes as durable tasks.",
	}
	root.AddCommand(workerCmd(), sandboxTLSCmd(), versionCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
