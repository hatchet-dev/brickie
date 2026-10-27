package main

import (
	"strconv"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/sandboxd"
	"github.com/hatchet-dev/brickie/internal/sandboxtls"
)

func sandboxTLSCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "sandbox-tls",
		Short: "Terminate mutual TLS for sandboxd inside a sandbox pod",
		RunE: func(*cobra.Command, []string) error {
			ctx, cancel := cmdutils.NewInterruptContext()
			defer cancel()
			return sandboxtls.Serve(ctx, dir, "127.0.0.1:"+strconv.Itoa(sandboxd.GRPCPort), "127.0.0.1:"+strconv.Itoa(sandboxd.RESTPort))
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "/etc/brickie/tls", "directory holding ca.crt, tls.crt, and tls.key")
	return cmd
}
