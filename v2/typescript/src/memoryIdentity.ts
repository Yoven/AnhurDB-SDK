/**
 * AnhurDB TypeScript SDK — who this `Memory` writes as: its container tag and
 * its current session uuid.
 *
 * One domain in one sentence: derive the container tag (from `userId`, or from
 * a hash of the API key), mint session uuids from it, and remember which
 * session is current and whether the server has registered it. Split out of
 * `memory.ts` (1532 lines, house cut ~300) in 4.0.0; the derivation rules are
 * unchanged byte-for-byte.
 *
 * Junior Tip [one owner for mutable session state]: the session uuid is read
 * by search, written by `createSession`/`newSession`, and checked by every
 * write. When those lived as loose fields on a 1500-line class, any method
 * could reassign them. Keeping them in this one object, which only the session
 * and write domains mutate, is what makes "which session will this write land
 * in?" answerable by reading one file.
 */

/**
 * Derive a short, stable hex tag from a string using the Web Crypto
 * SubtleCrypto API (available in Node 18+ and all modern runtimes).
 *
 * Falls back to a simple DJB2 hash when SubtleCrypto is unavailable
 * (e.g. some edge runtimes without `globalThis.crypto`). That fallback is a
 * RUNTIME capability check, not a server-version one.
 *
 *   Python: hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]
 *   Go:     hex.EncodeToString(sha256.Sum256([]byte(apiKey)))[:12]
 */
async function deriveTag(input: string): Promise<string> {
  try {
    const encoder = new TextEncoder();
    const data = encoder.encode(input);
    const hashBuffer = await crypto.subtle.digest("SHA-256", data);
    const hashArray = Array.from(new Uint8Array(hashBuffer));
    const hex = hashArray
      .map((byteValue) => byteValue.toString(16).padStart(2, "0"))
      .join("");
    return hex.slice(0, 12);
  } catch {
    // DJB2 for runtimes without SubtleCrypto.
    let hash = 5381;
    for (let charIndex = 0; charIndex < input.length; charIndex++) {
      hash = ((hash << 5) + hash + input.charCodeAt(charIndex)) >>> 0;
    }
    return hash.toString(16).padStart(8, "0").slice(0, 12);
  }
}

/** Format a Date as `YYYYMMDD-HHMMSS` in UTC. */
function utcTimestamp(moment: Date = new Date()): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return (
    `${moment.getUTCFullYear()}${pad(moment.getUTCMonth() + 1)}${pad(moment.getUTCDate())}` +
    `-${pad(moment.getUTCHours())}${pad(moment.getUTCMinutes())}${pad(moment.getUTCSeconds())}`
  );
}

/**
 * Generate a 6-lowercase-hex random suffix (3 crypto-random bytes) for the
 * default auto-derived session UUID.
 *
 * The default session UUID is `<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 hex>`.
 * The UTC timestamp alone collides for two sessions opened in the same
 * wall-clock second, so a 3-byte crypto-random suffix disambiguates them. The
 * 6-hex width and the crypto source MUST match the other SDKs byte-for-byte:
 * Python `secrets.token_hex(3)`, Go `crypto/rand` 3 bytes -> hex, TS
 * `crypto.getRandomValues` over a 3-byte `Uint8Array` -> hex.
 */
function randomHexSuffix(): string {
  const randomBytes = new Uint8Array(3);
  crypto.getRandomValues(randomBytes);
  return Array.from(randomBytes)
    .map((byteValue) => byteValue.toString(16).padStart(2, "0"))
    .join("");
}

/** A fresh local session uuid for `containerTag` (not yet registered). */
export function mintSessionUuid(containerTag: string): string {
  return `${containerTag}-${utcTimestamp()}-${randomHexSuffix()}`;
}

/**
 * The container tag and current session of one `Memory` instance.
 *
 * With `userId` the tag is known synchronously. Without it the tag is
 * `mem-` + the first 12 hex of SHA-256(apiKey), computed ASYNC; until `ready`
 * resolves the tag reads `"mem-init"` and the session uuid `""`.
 */
export class MemoryIdentity {
  private tag: string;
  /** Current session uuid (`""` until the async tag derivation lands). */
  sessionUuid: string;
  /** True only after a successful createSession/openSession (server ledger). */
  sessionRegistered = false;
  /** Resolves once the container tag (and default session) exist. */
  readonly ready: Promise<void>;

  constructor(apiKey: string, userId?: string) {
    if (userId) {
      this.tag = userId;
      this.sessionUuid = mintSessionUuid(userId);
      this.ready = Promise.resolve();
      return;
    }
    // Temporary tag — replaced exactly once when the async hash resolves.
    this.tag = "mem-init";
    this.sessionUuid = "";
    this.ready = deriveTag(apiKey).then((hash) => {
      this.tag = `mem-${hash}`;
      this.sessionUuid = mintSessionUuid(this.tag);
    });
  }

  /** The container tag every memory of this user/agent is grouped under. */
  get containerTag(): string {
    return this.tag;
  }
}
