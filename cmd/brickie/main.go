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
