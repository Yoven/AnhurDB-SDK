module github.com/anhurdb/anhur-hermes-memory

go 1.24

require github.com/anhurdb/anhur-memory-core v0.0.0

require github.com/anhurdb/sdk-go/v2 v2.0.0 // indirect

// Local dogfood: the engine (shared with the claude plugin) and the canonical SDK both ship in this
// repo. Replace directives only apply from the MAIN module, so each plugin must re-declare the SDK
// replace even though it reaches the SDK transitively through the core package.
replace github.com/anhurdb/anhur-memory-core => ../core

replace github.com/anhurdb/sdk-go/v2 => ../../golang
