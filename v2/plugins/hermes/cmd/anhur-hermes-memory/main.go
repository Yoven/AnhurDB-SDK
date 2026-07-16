// Command anhur-hermes-memory is the hook engine for the AnhurDB Hermes Code memory plugin.
//
// The engine itself lives in the shared package github.com/anhurdb/anhur-memory-core so the
// `claude` and `hermes` plugins never drift. This main is a thin wrapper: it hands core.Run the
// hermes plugin's identity (state dir + default container + binary name) and nothing else.
//
// Junior Tip [same engine, different identity, 2026-07-07]: this file is byte-for-byte the claude
// plugin's main EXCEPT for the three Config values below. That is the entire point of the shared
// core — the `hermes` plugin is the exact same tested engine pointed at a SEPARATE memory identity
// (tenant selected by ANHUR_API_KEY, container "hermes-ltm", state dir ~/.anhur-hermes-memory).
// Never fork engine logic in here; if behaviour must change, change it in the core package so BOTH
// plugins get it and neither drifts.
package main

import (
	"os"

	core "github.com/anhurdb/anhur-memory-core"
)

func main() {
	core.Run(os.Args, core.Config{
		StateDirName:     ".anhur-hermes-memory",
		DefaultContainer: "hermes-ltm",
		BinaryName:       "anhur-hermes-memory",
	})
}
