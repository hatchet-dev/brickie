package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

func (in *SandboxSession) DeepCopyInto(out *SandboxSession) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	if in.Spec.TTLSeconds != nil {
		v := *in.Spec.TTLSeconds
		out.Spec.TTLSeconds = &v
	}
}

func (in *SandboxSession) DeepCopy() *SandboxSession {
	if in == nil {
		return nil
	}
	out := new(SandboxSession)
	in.DeepCopyInto(out)
	return out
}

func (in *SandboxSession) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *SandboxSessionList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(SandboxSessionList)
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = make([]SandboxSession, len(in.Items))
	for i := range in.Items {
		in.Items[i].DeepCopyInto(&out.Items[i])
	}
	return out
}

func (in *WorkspaceSnapshot) DeepCopyInto(out *WorkspaceSnapshot) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
}

func (in *WorkspaceSnapshot) DeepCopy() *WorkspaceSnapshot {
	if in == nil {
		return nil
	}
	out := new(WorkspaceSnapshot)
	in.DeepCopyInto(out)
	return out
}

func (in *WorkspaceSnapshot) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *WorkspaceSnapshotList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(WorkspaceSnapshotList)
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = make([]WorkspaceSnapshot, len(in.Items))
	for i := range in.Items {
		in.Items[i].DeepCopyInto(&out.Items[i])
	}
	return out
}
