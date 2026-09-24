package main

import (
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "brickie",
		Short: "Agent sandboxes as durable tasks. Run `brickie dev` to try it on this machine.",
	}
	root.AddCommand(devCmd())
	for _, hidden := range []*cobra.Command{workerCmd(), csiCmd(), injectorCmd(), bootstrapKindCmd(), bootstrapAWSCmd()} {
		hidden.Hidden = true
		root.AddCommand(hidden)
	}
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
