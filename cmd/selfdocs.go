package cmd

import (
	"fmt"
	"io/fs"
	"os"

	"ai-agent-go-play/internal/selfdocs"
)

// selfDocs is the agent's embedded documentation, set once from package main (which owns
// the //go:embed) via SetSelfDocs and passed into every executor so the agent can read
// about itself with the read_self_docs tool. Nil until set, and nil is fine — the
// executor simply omits the tool.
var selfDocs *selfdocs.Docs

// SetSelfDocs builds the doc set from the embedded filesystem supplied by main. A parse
// failure is non-fatal: the agent runs without read_self_docs rather than not at all.
func SetSelfDocs(fsys fs.FS) {
	docs, err := selfdocs.New(fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "self-docs: %v — running without read_self_docs\n", err)
		return
	}
	selfDocs = docs
}
