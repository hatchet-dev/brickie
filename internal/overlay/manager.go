package overlay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manager implements the brickie-node workspace lifecycle from the design
// notes: mount a lowerdir chain with a fresh upper, checkpoint the upper as a
// new content-addressed layer, fork by reusing a chain with a fresh upper.
type Manager struct {
	Root  string
	Store *Store
}

// maxChainDepth bounds the overlay lowerdir chain. The kernel caps the mount
// option string at one page (~4KB) on older kernels, and each layer path is
// ~110 bytes, so a chain deeper than ~35 overflows and the mount fails with
// ENOENT on a truncated path. When a checkpoint would exceed this, the whole
// workspace is squashed into one fresh base layer instead of appending.
const maxChainDepth = 20

func NewManager(root string) *Manager {
	return &Manager{Root: root, Store: &Store{Root: root}}
}

func (m *Manager) sandboxDir(id string) string {
	return filepath.Join(m.Root, "sandboxes", id)
}

func (m *Manager) paths(id string) (upper, work, merged string) {
	base := m.sandboxDir(id)
	return filepath.Join(base, "upper"), filepath.Join(base, "work"), filepath.Join(base, "merged")
}

func (m *Manager) chainPath(id string) string {
	return filepath.Join(m.sandboxDir(id), "chain.json")
}

func (m *Manager) readChain(id string) ([]string, error) {
	b, err := os.ReadFile(m.chainPath(id))
	if err != nil {
		return nil, err
	}
	var chain []string
	return chain, json.Unmarshal(b, &chain)
}

func (m *Manager) writeChain(id string, chain []string) error {
	b, _ := json.Marshal(chain)
	return os.WriteFile(m.chainPath(id), b, 0o644)
}

func (m *Manager) emptyBaseLayer() (string, error) {
	dir := filepath.Join(m.Root, "layers", "empty")
	return dir, os.MkdirAll(dir, 0o755)
}

// lowerdirOption builds the overlay lowerdir string. Manifests store layers
// oldest first; overlay wants the topmost lower first.
func (m *Manager) lowerdirOption(chain []string) (string, error) {
	if len(chain) == 0 {
		empty, err := m.emptyBaseLayer()
		return empty, err
	}
	dirs := make([]string, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		if err := m.Store.Materialize(chain[i]); err != nil {
			return "", err
		}
		dirs = append(dirs, m.Store.LayerDir(chain[i]))
	}
	return strings.Join(dirs, ":"), nil
}

// Mount creates the sandbox workspace from a snapshot (empty chain when
// snapshotID is ""). Fork is this same call with a new sandbox id.
func (m *Manager) Mount(sandboxID, snapshotID string) (string, error) {
	if err := m.Store.EnsureDirs(); err != nil {
		return "", err
	}
	chain := []string{}
	if snapshotID != "" {
		var err error
		chain, err = m.Store.ReadManifest(snapshotID)
		if err != nil {
			return "", err
		}
	}
	upper, work, merged := m.paths(sandboxID)
	for _, dir := range []string{upper, work, merged} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	lower, err := m.lowerdirOption(chain)
	if err != nil {
		return "", err
	}
	if err := mountOverlay(lower, upper, work, merged); err != nil {
		return "", fmt.Errorf("mount overlay for %s: %w", sandboxID, err)
	}
	if err := m.writeChain(sandboxID, chain); err != nil {
		return "", err
	}
	return merged, nil
}

// Checkpoint freezes the current upper as a new layer and records a snapshot
// manifest. The overlay stays mounted: the upperdir is a plain host directory
// holding exactly this turn's diff, so it is tarred in place after a syncfs
// flush. The mount is torn down later by Unmount when the pod is deleted, and
// the next turn is a fresh mount, so there is no remount here.
func (m *Manager) Checkpoint(sandboxID, snapshotID string) ([]string, error) {
	chain, err := m.readChain(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("sandbox %s is not mounted: %w", sandboxID, err)
	}
	upper, _, merged := m.paths(sandboxID)

	if err := syncFS(merged); err != nil {
		return nil, err
	}
	if len(chain) >= maxChainDepth {
		// Squash: freeze the full merged view as a single base layer so the
		// chain never grows past the kernel's mount-option limit.
		digest, err := m.Store.PutUpper(merged)
		if err != nil {
			return nil, err
		}
		chain = []string{digest}
	} else {
		digest, err := m.Store.PutUpper(upper)
		if err != nil {
			return nil, err
		}
		chain = append(chain, digest)
	}
	if err := m.Store.WriteManifest(snapshotID, chain); err != nil {
		return nil, err
	}
	return chain, nil
}

func (m *Manager) Unmount(sandboxID string) error {
	_, _, merged := m.paths(sandboxID)
	if err := unmountOverlay(merged); err != nil {
		return err
	}
	return os.RemoveAll(m.sandboxDir(sandboxID))
}

func (m *Manager) MergedPath(sandboxID string) string {
	_, _, merged := m.paths(sandboxID)
	return merged
}
