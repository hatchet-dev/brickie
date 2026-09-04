//go:build !linux

package overlay

import "errors"

var errLinuxOnly = errors.New("overlay workspaces are only supported on linux nodes")

func mountOverlay(lower, upper, work, target string) error { return errLinuxOnly }
func unmountOverlay(target string) error                    { return errLinuxOnly }
func bindMount(source, target string) error                 { return errLinuxOnly }
func unmountBind(target string) error                       { return errLinuxOnly }
func syncFS(mountpoint string) error                        { return errLinuxOnly }
