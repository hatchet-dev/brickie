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
	worker := workerCmd()
	worker.Hidden = true
	root.AddCommand(worker)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
