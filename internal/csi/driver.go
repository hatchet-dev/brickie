package csi

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"

	"github.com/hatchet-dev/brickie/internal/overlay"
)

type Options struct {
	Endpoint string
	NodeID   string
	DataDir  string
	HTTPAddr string
	Remote   overlay.ObjectStore
}

// Serve runs the CSI node plugin (gRPC on a unix socket for kubelet) and a
// small HTTP API used by the provider to checkpoint a live workspace between
// turns without unmounting it from the pod.
func Serve(opts Options) error {
	manager := overlay.NewManager(opts.DataDir)
	manager.Store.Remote = opts.Remote

	addr := strings.TrimPrefix(opts.Endpoint, "unix://")
	if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return err
	}
	listener, err := net.Listen("unix", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	server := grpc.NewServer()
	csipb.RegisterIdentityServer(server, identityServer{})
	csipb.RegisterNodeServer(server, nodeServer{nodeID: opts.NodeID, dataDir: opts.DataDir, manager: manager})

	if opts.HTTPAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /v1/checkpoint", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				SandboxID  string `json:"sandboxId"`
				SnapshotID string `json:"snapshotId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SandboxID == "" || req.SnapshotID == "" {
				http.Error(w, "sandboxId and snapshotId are required", http.StatusBadRequest)
				return
			}
			layers, err := manager.Checkpoint(req.SandboxID, req.SnapshotID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"snapshotId": req.SnapshotID, "layers": layers})
		})
		go func() {
			fmt.Fprintf(os.Stderr, "brickie: csi node http api on %s\n", opts.HTTPAddr)
			if err := http.ListenAndServe(opts.HTTPAddr, mux); err != nil {
				fmt.Fprintf(os.Stderr, "brickie: csi http api: %v\n", err)
			}
		}()
	}

	fmt.Fprintf(os.Stderr, "brickie: csi node driver %s listening on %s\n", DriverName, opts.Endpoint)
	return server.Serve(listener)
}
