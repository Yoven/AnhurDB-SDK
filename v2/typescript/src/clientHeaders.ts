/**
 * The headers every AnhurDB request carries, and the injection guard that keeps
 * them safe.
 *
 * Domain, in one sentence: what identifies this client to the server, and what
 * a caller-supplied string has to survive before it is allowed into a header.
 *
 * Junior Tip [why this left client.ts, 2026-09-05]: `client.ts` was 405 lines
 * against this project's ~300-line cut, and the previous pass GREW it by three
 * (the `USER_AGENT` import and its use). House law forbids growing a file that
 * is already past the cut — the part you touch gets split out first, then
 * changed. The seam is not arbitrary: header construction is a responsibility a
 * junior can name in one sentence ("what goes in the headers and what is
 * refused"), while what stays in `client.ts` is a different sentence ("make one
 * fetch attempt and map the outcome to a typed error").
 *
 * Junior Tip [why the CRLF guard sits beside the header map and not in a
 * `utils.ts`]: it exists only because these three values (API key, tenant id,
 * user agent) end up in an HTTP header. Separating the rule from the place it
 * protects is how a later refactor adds a fourth header and forgets to validate
 * it — the check has to live where the header is built.
 */

import { USER_AGENT } from "./version.js";

/**
 * Printable ASCII only. CR, LF, NUL and every other control character are
 * rejected, because a header value containing CRLF can terminate the header
 * block early and inject a header (or a whole response) of the attacker's
 * choosing — HTTP response splitting.
 */
const HEADER_SAFE = /^[\x20-\x7e]*$/;

/**
 * Validate that a string is safe to use as an HTTP header value.
 *
 * @param value - The caller-supplied value.
 * @param name  - The parameter name, so the error names what to fix.
 * @throws {Error} when the value contains anything outside printable ASCII.
 */
export function validateHeaderValue(value: string, name: string): void {
  if (value && !HEADER_SAFE.test(value)) {
    throw new Error(
      `${name} contains invalid characters for HTTP header. ` +
        "Only printable ASCII (0x20-0x7E) is allowed.",
    );
  }
}

/**
 * Build the header map shared by every request this client makes.
 *
 * `Content-Type` is deliberately NOT set here: FormData uploads need the
 * runtime to set `multipart/form-data` together with the boundary it generates,
 * so JSON requests add the header per call instead.
 *
 * Junior Tip [why User-Agent comes from version.ts]: a hand-typed literal is
 * exactly how the wire version once drifted three releases away from
 * `package.json`. One source of truth, imported — never a copy.
 *
 * @param apiKey   - Sent as `X-API-Key`; validated against header injection.
 * @param tenantId - Optional `X-Tenant-ID`; validated the same way.
 * @returns A fresh map, so the caller can mutate it without affecting anyone else.
 */
export function buildRequestHeaders(apiKey: string, tenantId?: string): Record<string, string> {
  validateHeaderValue(apiKey, "apiKey");
  if (tenantId) validateHeaderValue(tenantId, "tenantId");

  const headers: Record<string, string> = {
    "X-API-Key": apiKey,
    "User-Agent": USER_AGENT,
  };
  if (tenantId) {
    headers["X-Tenant-ID"] = tenantId;
  }
  return headers;
}
