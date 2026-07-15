module github.com/anhurdb/anhur-memory-core

go 1.24

require github.com/anhurdb/sdk-go/v2 v2.0.0

// Local dogfood: build against the canonical SDK that ships in this repo (../../golang). For a
// standalone/published build, drop this replace and `go get github.com/anhurdb/sdk-go/v2`.
replace github.com/anhurdb/sdk-go/v2 => ../../golang
