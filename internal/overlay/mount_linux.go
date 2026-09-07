//go:build linux

package overlay

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func mountOverlay(lower, upper, work, target string) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	return unix.Mount("overlay", target, "overlay", 0, opts)
}

func unmountOverlay(target string) error {
	return unmountIdempotent(target)
}

func bindMount(source, target string) error {
	return unix.Mount(source, target, "", unix.MS_BIND, "")
}

func unmountBind(target string) error {
	return unmountIdempotent(target)
}

func unmountIdempotent(target string) error {
	err := unix.Unmount(target, 0)
	if err == unix.EBUSY {
		err = unix.Unmount(target, unix.MNT_DETACH)
	}
	if err == unix.EINVAL || err == unix.ENOENT {
		return nil
	}
	return err
}

func syncFS(mountpoint string) error {
	f, err := os.Open(mountpoint)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}
