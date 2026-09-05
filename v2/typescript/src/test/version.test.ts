/**
 * The version this SDK claims must be ONE number.
 *
 * Before 2.1.0 there were five: `package.json` said 2.0.0, the lockfile 2.0.0,
 * the `User-Agent` literal 2.1, the README tarball 2.0.10 and the portal
 * 2.0.14. Nothing compared them, so nothing failed. This file is the compare.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { SDK_VERSION, USER_AGENT } from "../version.js";
import { HttpClient } from "../client.js";

/**
 * `package.json`, read from the compiled test's location
 * (`dist/test/test/version.test.js` → three levels up = the package root).
 */
function readPackageManifest(): { version: string } {
  const manifestPath = join(__dirname, "..", "..", "..", "package.json");
  return JSON.parse(readFileSync(manifestPath, "utf-8")) as { version: string };
}

describe("SDK version", () => {
  it("matches package.json exactly", () => {
    assert.equal(SDK_VERSION, readPackageManifest().version);
  });

  it("is the 2.1.0 the three SDKs converged on", () => {
    assert.equal(SDK_VERSION, "2.1.0");
  });

  it("is full semver, not a truncated major.minor", () => {
    assert.match(SDK_VERSION, /^\d+\.\d+\.\d+$/);
  });

  it("builds the User-Agent from the same constant", () => {
    assert.equal(USER_AGENT, `AnhurSDK-TypeScript/${SDK_VERSION}`);
  });

  it("puts that exact User-Agent on the wire", async () => {
    const originalFetch = globalThis.fetch;
    let sentUserAgent: string | undefined;
    globalThis.fetch = (async (_requestUrl: unknown, init: RequestInit) => {
      sentUserAgent = (init.headers as Record<string, string>)["User-Agent"];
      return new Response("{}", {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;
    try {
      await new HttpClient("http://localhost:8000", "key").get("/api/v1/health");
    } finally {
      globalThis.fetch = originalFetch;
    }
    assert.equal(sentUserAgent, USER_AGENT);
  });
});
