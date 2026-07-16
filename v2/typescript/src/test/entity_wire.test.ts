import { describe, it } from "node:test";
import assert from "node:assert/strict";
import type { EntityRecord } from "../types.js";

describe("EntityRecord wire contract", () => {
  it("models entity_type (not type) from AnhurDB entityToResponse", () => {
    const wire = {
      id: 42,
      name: "chrome",
      entity_type: "product",
      mention_count: 7,
      weight: 1,
    };
    const entity = wire as EntityRecord;
    assert.equal(entity.entity_type, "product");
    assert.equal(entity.name, "chrome");
    assert.equal(entity.mention_count, 7);
    assert.equal("type" in entity && (entity as { type?: string }).type !== undefined, false);
  });
});
