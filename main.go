package main

import (
	"embed"

	"ai-agent-go-play/cmd"
)

// selfDocsFS embeds the agent's own documentation so it can read it at runtime via the
// read_self_docs tool, regardless of working directory (and in a deployed single
// binary). The set is the README, the reference docs (docs/*.md — a flat glob that does
// NOT descend into docs/planning/), and the vision doc. Planning/scratchpad docs are
// deliberately excluded so the agent doesn't mistake roadmap for current behavior.
//
//go:embed README.md docs/*.md self-extending-agent-design.md
var selfDocsFS embed.FS

func main() {
	cmd.SetSelfDocs(selfDocsFS)
	cmd.Execute()
}
