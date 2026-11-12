// Command proxyfile writes a proxy's configuration from a proxyfile.
package main

import (
	"os"
	"runtime/debug"

	"github.com/kitecorp/proxyfile/v2/internal/cli"
)

// version is set by the release build: -ldflags "-X main.version=<version>".
var version string

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, programVersion()))
}

// programVersion is the release build's version, else the module version the go command recorded in the program
// (go install github.com/kitecorp/proxyfile/v2/cmd/proxyfile@v2.0.0 records v2.0.0), else (devel).
func programVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
