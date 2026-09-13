//go:build !linux

package overlay

import "errors"

var errLinuxOnly = errors.New("overlay workspaces are only supported on linux nodes")

func mountOverlay(_, _, _, _ string) error { return errLinuxOnly }
func unmountOverlay(_ string) error        { return errLinuxOnly }
func bindMount(_ string, _ string) error   { return errLinuxOnly }
func unmountBind(_ string) error           { return errLinuxOnly }
func syncFS(_ string) error                { return errLinuxOnly }
