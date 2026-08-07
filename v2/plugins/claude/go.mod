module github.com/anhurdb/anhur-claude-memory

go 1.24.0

toolchain go1.24.4

require github.com/anhurdb/anhur-memory-core v0.0.0

require (
	github.com/Yoven/AnhurDB-SDK/v2/golang/v2 v2.0.6 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	golang.org/x/sys v0.37.0 // indirect
	modernc.org/libc v1.67.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.44.0 // indirect
)

// Local dogfood: the engine (shared with the hermes plugin) and the canonical SDK both ship in this
// repo. Replace directives only apply from the MAIN module, so each plugin must re-declare the SDK
// replace even though it reaches the SDK transitively through the core package.
replace github.com/anhurdb/anhur-memory-core => ../core

replace github.com/Yoven/AnhurDB-SDK/v2/golang/v2 => ../../golang
