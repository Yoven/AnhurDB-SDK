// Command anhur-claude-memory is the hook engine for the AnhurDB Claude Code memory plugin.
//
// The engine itself lives in the shared package github.com/anhurdb/anhur-memory-core so the
// `claude` and `hermes` plugins never drift. This main is a thin wrapper: it hands core.Run the
// claude plugin's identity (state dir + default container + binary name) and nothing else.
//
// Junior Tip [behaviour-preserving, 2026-07-07]: these three Config values reproduce the exact
// defaults the engine used to hardcode (~/.anhur-claude-memory, container "claude-ltm", the usage
// string). They MUST stay byte-identical — this binary backs the user's live long-term memory, and
// changing them would silently repoint or rename that memory. Everything tunable is env-driven
// (ANHUR_*), not baked in here.
package main

import (
	"os"

	core "github.com/anhurdb/anhur-memory-core"
)

func main() {
	core.Run(os.Args, core.Config{
		StateDirName:     ".anhur-claude-memory",
		DefaultContainer: "claude-ltm",
		BinaryName:       "anhur-claude-memory",
	})
}
