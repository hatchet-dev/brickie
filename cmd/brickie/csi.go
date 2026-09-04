package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/hatchet-dev/brickie/internal/csi"
	"github.com/hatchet-dev/brickie/internal/overlay"
)

func csiCmd() *cobra.Command {
	var (
		endpoint    string
		nodeID      string
		dataDir     string
		httpAddr    string
		objectStore string
	)
	cmd := &cobra.Command{
		Use:   "csi-node",
		Short: "Run the brickie CSI node plugin (overlayfs workspaces for sandbox pods)",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := csi.Options{Endpoint: endpoint, NodeID: nodeID, DataDir: dataDir, HTTPAddr: httpAddr}
			if objectStore != "" {
				remote, err := overlay.NewBucketStore(context.Background(), objectStore)
				if err != nil {
					return err
				}
				opts.Remote = remote
			}
			return csi.Serve(opts)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", envOr("CSI_ENDPOINT", "unix:///csi/csi.sock"), "CSI gRPC endpoint")
	cmd.Flags().StringVar(&nodeID, "node-id", envOr("KUBE_NODE_NAME", "unknown"), "kubernetes node name")
	cmd.Flags().StringVar(&dataDir, "data-dir", envOr("BRICKIE_DATA_DIR", "/var/lib/brickie"), "host directory for layers, sandboxes, and manifests")
	cmd.Flags().StringVar(&httpAddr, "http-addr", envOr("BRICKIE_CSI_HTTP_ADDR", ":7078"), "checkpoint HTTP API address (empty to disable)")
	cmd.Flags().StringVar(&objectStore, "object-store", envOr("BRICKIE_OBJECT_STORE", ""), "object store URL for layer blobs and manifests: s3://, gs://, azblob://, or s3:// with ?endpoint= for any S3-compatible store")
	return cmd
}
