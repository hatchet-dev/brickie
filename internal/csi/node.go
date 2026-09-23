package csi

import (
	"context"
	"os"
	"path/filepath"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hatchet-dev/brickie/internal/overlay"
)

const (
	AttrSandboxID  = "brickie.hatchet.run/sandbox-id"
	AttrSnapshotID = "brickie.hatchet.run/snapshot-id"
)

type nodeServer struct {
	csipb.UnimplementedNodeServer
	nodeID  string
	dataDir string
	manager *overlay.Manager
}

func (n nodeServer) volumeMapPath(volumeID string) string {
	return filepath.Join(n.dataDir, "volume-map", volumeID)
}

func (n nodeServer) NodeGetInfo(context.Context, *csipb.NodeGetInfoRequest) (*csipb.NodeGetInfoResponse, error) {
	return &csipb.NodeGetInfoResponse{NodeId: n.nodeID}, nil
}

func (n nodeServer) NodeGetCapabilities(context.Context, *csipb.NodeGetCapabilitiesRequest) (*csipb.NodeGetCapabilitiesResponse, error) {
	return &csipb.NodeGetCapabilitiesResponse{}, nil
}

// NodePublishVolume mounts a workspace for a sandbox pod as a CSI ephemeral
// volume: overlay chain from the snapshot in the volume context, fresh upper,
// bind-mounted onto the kubelet target path. A fork is just a second publish
// with the same snapshot id and a different sandbox id.
func (n nodeServer) NodePublishVolume(ctx context.Context, req *csipb.NodePublishVolumeRequest) (*csipb.NodePublishVolumeResponse, error) {
	if req.VolumeId == "" || req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and target path are required")
	}
	sandboxID := req.GetVolumeContext()[AttrSandboxID]
	if sandboxID == "" {
		sandboxID = req.GetVolumeContext()["csi.storage.k8s.io/pod.name"]
	}
	if sandboxID == "" {
		sandboxID = req.VolumeId
	}
	snapshotID := req.GetVolumeContext()[AttrSnapshotID]

	merged, err := n.manager.Mount(sandboxID, snapshotID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.MkdirAll(req.TargetPath, 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := overlay.BindMount(merged, req.TargetPath); err != nil {
		_ = n.manager.Unmount(sandboxID)
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(n.volumeMapPath(req.VolumeId)), 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.WriteFile(n.volumeMapPath(req.VolumeId), []byte(sandboxID), 0o644); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &csipb.NodePublishVolumeResponse{}, nil
}

func (n nodeServer) NodeUnpublishVolume(ctx context.Context, req *csipb.NodeUnpublishVolumeRequest) (*csipb.NodeUnpublishVolumeResponse, error) {
	if req.VolumeId == "" || req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and target path are required")
	}
	sandboxID := req.VolumeId
	if b, err := os.ReadFile(n.volumeMapPath(req.VolumeId)); err == nil && len(b) > 0 {
		sandboxID = string(b)
	}
	if err := overlay.UnmountBind(req.TargetPath); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := n.manager.Unmount(sandboxID); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	_ = os.Remove(n.volumeMapPath(req.VolumeId))
	return &csipb.NodeUnpublishVolumeResponse{}, nil
}
