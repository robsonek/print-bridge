// Package version exposes the build version, injected via ldflags at release.
package version

// Version is overridden at build: -ldflags "-X github.com/robsonek/print-bridge/internal/version.Version=1.2.3"
var Version = "dev"
