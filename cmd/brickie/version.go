package main

import (
	"crypto/fips140"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Set by the release build through -ldflags; a plain go build reports what the
// module's build info knows.
var (
	version = ""
	commit  = ""
	date    = ""
)

func versionInfo() (v, c, d string) {
	v, c, d = version, commit, date
	info, ok := debug.ReadBuildInfo()
	if v == "" && ok {
		v = info.Main.Version
	}
	if c == "" && ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				c = s.Value
			}
		}
	}
	if v == "" {
		v = "(devel)"
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return v, c, d
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the brickie version and build details",
		Run: func(cmd *cobra.Command, _ []string) {
			v, c, d := versionInfo()
			fips := "off"
			if fips140.Enabled() {
				fips = "on, Go Cryptographic Module " + fips140.Version()
			}
			cmd.Printf("brickie %s\ncommit  %s\nbuilt   %s\ngo      %s %s/%s\nfips    %s\n", v, c, d, runtime.Version(), runtime.GOOS, runtime.GOARCH, fips)
		},
	}
}
