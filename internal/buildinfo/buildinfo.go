// Package buildinfo exposes the binary's build identity so the agent can report its
// own version (part of self-awareness). Version is overridable at link time, e.g.
//
//	go build -ldflags "-X ai-agent-go-play/internal/buildinfo.Version=$(git describe --tags --always)"
package buildinfo

// Version is the build version, "dev" for an unstamped local build.
var Version = "dev"
