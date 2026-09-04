package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func metav1AddToGroupVersion(scheme *runtime.Scheme) {
	metav1.AddToGroupVersion(scheme, GroupVersion)
}

type SandboxSessionSpec struct {
	Image        string `json:"image,omitempty"`
	FromSnapshot string `json:"fromSnapshot,omitempty"`
	TTLSeconds   *int64 `json:"ttlSeconds,omitempty"`
}

type SandboxSessionPhase string

const (
	SessionPending SandboxSessionPhase = "Pending"
	SessionReady   SandboxSessionPhase = "Ready"
	SessionExpired SandboxSessionPhase = "Expired"
)

type SandboxSessionStatus struct {
	Phase          SandboxSessionPhase `json:"phase,omitempty"`
	SandboxName    string              `json:"sandboxName,omitempty"`
	LastSnapshotID string              `json:"lastSnapshotId,omitempty"`
}

type SandboxSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SandboxSessionSpec   `json:"spec"`
	Status SandboxSessionStatus `json:"status,omitempty"`
}

type SandboxSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxSession `json:"items"`
}

type WorkspaceSnapshotSpec struct {
	SessionRef string `json:"sessionRef,omitempty"`
	TurnRunID  string `json:"turnRunId,omitempty"`
}

type WorkspaceSnapshotStatus struct {
	Ready     bool   `json:"ready,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Location  string `json:"location,omitempty"`
}

type WorkspaceSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSnapshotSpec   `json:"spec"`
	Status WorkspaceSnapshotStatus `json:"status,omitempty"`
}

type WorkspaceSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceSnapshot `json:"items"`
}
