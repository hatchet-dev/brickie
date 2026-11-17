// Every build of brickie runs in FIPS 140-3 mode; release builds also pin the
// certified Go Cryptographic Module (see .goreleaser.yml and Dockerfile).
//
//go:debug fips140=on
package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/logging"
)

func main() {
	var logFormat string
	root := &cobra.Command{
		Use:           "brickie",
		Short:         "Agent sandboxes as durable tasks.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return logging.Setup(logFormat)
		},
	}
	root.PersistentFlags().StringVar(&logFormat, "log-format", envOr("BRICKIE_LOG_FORMAT", "json"), "log lines as json or text")
	root.AddCommand(workerCmd(), sandboxTLSCmd(), versionCmd())
	if err := root.Execute(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}
