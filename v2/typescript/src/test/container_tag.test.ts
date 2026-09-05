/**
 * `containerTag` — the key every memory of a user/agent is grouped under.
 *
 * Go exposes it as `Memory.ContainerTag()` and Python as the
 * `Memory.container_tag` property; TypeScript did not expose it at all, so a
 * caller could not tell which tag their writes were landing under. These tests
 * pin the two shapes (sync getter, async getter) and, above all, the derived
 * value — a tag that silently stayed `"mem-init"` would scatter one user's
 * memories across two containers.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";

describe("Memory.containerTag", () => {
  it("is the explicit userId when one was given", () => {
    const memory = new Memory({ apiKey: "key", userId: "user-42" });
    assert.equal(memory.containerTag, "user-42");
  });

  it("resolves to the mem-<hash> tag derived from the API key", async () => {
    const memory = new Memory({ apiKey: "test-key-123" });
    const resolvedTag = await memory.getContainerTag();
    assert.match(resolvedTag, /^mem-[0-9a-f]{8,12}$/);
    // The sync getter agrees once derivation has landed.
    assert.equal(memory.containerTag, resolvedTag);
  });

  it("is the same tag the session uuid was built from", async () => {
    const memory = new Memory({ apiKey: "test-key-123" });
    const resolvedTag = await memory.getContainerTag();
    const sessionId = await memory.getSessionId();
    assert.ok(
      sessionId.startsWith(`${resolvedTag}-`),
      `session ${sessionId} must be namespaced by the container tag ${resolvedTag}`,
    );
  });

  it("is stable across two clients holding the same API key", async () => {
    const firstClient = new Memory({ apiKey: "same-key" });
    const secondClient = new Memory({ apiKey: "same-key" });
    assert.equal(await firstClient.getContainerTag(), await secondClient.getContainerTag());
  });
});
