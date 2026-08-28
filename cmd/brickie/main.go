package main

import (
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "brickie",
		Short: "Universal sandbox API and session manager. Deploys into your own cloud account.",
	}
	root.AddCommand(workerCmd(), bootstrapLocalCmd(), bootstrapKindCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
