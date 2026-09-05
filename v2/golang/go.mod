module github.com/Yoven/AnhurDB-SDK/v2/golang/v2

// go 1.24 is the LANGUAGE version this library requires — deliberately the
// oldest Go it actually compiles under, because a library's `go` directive is a
// floor imposed on every consumer (the Claude/Hermes plugins, AnhurAgents, and
// any customer importing this SDK). Raising it without needing a 1.25+ feature
// would force all of them to upgrade for nothing.
//
// toolchain go1.26.0 is the version WE build and test with, and it matches
// .tool-versions in this directory. The two lines used to look like a
// contradiction (.tool-versions said 1.26.0, go.mod said 1.24) because nothing
// wrote down that they answer different questions: `go` = the minimum a
// consumer needs, `toolchain` = what the maintainers run. A toolchain directive
// is ignored when this module is a dependency, so it cannot leak the floor.
go 1.24

toolchain go1.26.0
