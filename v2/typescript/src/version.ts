/**
 * AnhurDB TypeScript SDK — the ONE place this package states its version.
 *
 * Junior Tip [why a constant instead of a literal in the header]: before
 * 2.1.0 the `User-Agent` carried a hand-typed `"AnhurSDK-TypeScript/2.1"`
 * while `package.json` said `2.0.0` and the published tarball said `2.0.10`.
 * Nothing compared them, so the number the SERVER saw in its access logs was
 * a number no release had ever carried — and every attempt to answer "which
 * SDK is that client running?" from telemetry was answering with a literal
 * someone forgot to bump. One constant, one release ritual: bump it here and
 * in `package.json`, and `version.test.ts` fails the build if the two drift.
 *
 * The version is NOT read from `package.json` at runtime: the published
 * package ships only `dist/` (see `.npmignore` / the `files` field), and a
 * bundler inlining the SDK has no `package.json` to resolve at all.
 *
 * @module
 */

/**
 * Semantic version of this SDK, identical to `package.json`'s `version`.
 *
 * Kept in lockstep with the Go and Python SDKs — the three ship the same
 * number in the same change (`feedback_sdk_sync_invariant`).
 */
export const SDK_VERSION = "2.1.0";

/**
 * Value sent as `User-Agent` on every HTTP request.
 *
 * Full semver, not `major.minor`: a truncated User-Agent cannot distinguish
 * a patch that changed wire behaviour from one that did not, which is the
 * whole reason a server keeps this header.
 */
export const USER_AGENT = `AnhurSDK-TypeScript/${SDK_VERSION}`;
