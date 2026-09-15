package client

// version.go — the single place the Go SDK states its own version.
//
// Junior Tip [why a constant instead of a literal in the header, 2026-09-05]:
// until this file existed the Go SDK carried NO version symbol at all. The one
// runtime-observable version was a string literal inside setAuthHeaders
// ("AnhurSDK-Golang/2.1"), which drifted away from every other truth in the
// repository: the README pinned an older tag, the CHANGELOG named a newer one,
// and the plugin go.mod required a third. Nobody could answer "which SDK is
// talking to my server?" from one place, and server-side traffic analysis by
// User-Agent was reading a version that matched no published artefact.
//
// The rule this file enforces: the User-Agent is DERIVED from Version, never
// typed again. A release bumps exactly one line here, and the wire follows.
// If you ever find yourself writing "AnhurSDK-Golang/" anywhere else, that is
// the bug — the drift starts the moment the string exists twice.

// Version is the semantic version of this Go SDK source tree.
//
// Converged with the TypeScript and Python SDKs on 2.1.0 (2026-09-05): all
// three previously claimed 2.1 on the wire while their manifests said 2.0.0
// and their published tags were 2.0.17/2.0.18/2.0.20. 2.1.0 sits above every
// shipped tag and makes the wire-visible claim true instead of aspirational.
//
// 3.0.0 (2026-09-14) is the SDK parity release. It is a MAJOR bump because it
// removes surface: fourteen methods lost an option parameter they were
// discarding, three model types that described contracts the server does not
// speak were deleted, SmartSearch stopped returning raw bytes and
// SearchWithRetrieval stopped returning a tuple that could not carry
// leg_scores. Every removal is a compile error, by design — the old code
// compiled and lied.
const Version = "3.0.1"

// UserAgentPrefix names the SDK in the User-Agent header. Kept separate from
// Version so the two concerns (who I am / which release I am) never get
// concatenated by hand at a call site.
const UserAgentPrefix = "AnhurSDK-Golang/"

// UserAgent is the exact User-Agent header value every request carries.
const UserAgent = UserAgentPrefix + Version
