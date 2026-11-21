/**
 * Shapes of the client-key surface (`/api/v1/client/*`, ADR-0034).
 *
 * One domain: the request options and response objects of `ClientAPI`.
 * Responses keep the server's snake_case JSON names (this SDK's convention),
 * read from the AnhurDB handlers (`server/handler/client_*.go`); request
 * options are camelCase like every other TypeScript option bag. Parity: Go
 * `client/client_key_types.go`, Python `anhurdb/client/client_key_types.py`.
 */

/** The RBAC scopes an impersonation token can be minted with
 * (`validRequestedScope`, `server/handler/client_impersonate.go`). */
export type KeyScope = "admin" | "write" | "read_only";

/** Named constants for {@link KeyScope}. */
export const KEY_SCOPE_ADMIN: KeyScope = "admin";
export const KEY_SCOPE_WRITE: KeyScope = "write";
export const KEY_SCOPE_READ_ONLY: KeyScope = "read_only";

const SCOPE_RANK: Readonly<Record<string, number>> = { read_only: 1, write: 2, admin: 3 };

/**
 * Order scopes read_only < write < admin; 0 means unknown.
 *
 * Junior Tip [why the SDK ranks scopes]: the server never refuses a scope
 * above the caller's ceiling — it silently LOWERS it. A scope ABOVE the one
 * requested can only come from a bug or an impostor, and handing that token to
 * an agent would grant more than asked. Ranking accepts "lower" and refuses
 * "higher" without string matching.
 */
export function scopeRank(scope: string): number {
  return Object.prototype.hasOwnProperty.call(SCOPE_RANK, scope) ? SCOPE_RANK[scope] : 0;
}

/** One child client (`childSummary`, `server/handler/client_children.go`).
 * `created` is true only on the call that created it (HTTP 201) and always
 * false in `listChildren`. */
export interface ChildClient {
  child_client_id: string;
  external_ref: string;
  status: string;
  created: boolean;
  created_at: string;
}

/** Answer of suspend/resume: the status re-read after the change. */
export interface ChildStatus {
  child_client_id: string;
  status: string;
}

/**
 * Optional per-tenant configuration (`normalizeAgentProfile`,
 * `server/handler/client_tenant_profile.go`). `config` takes only the 16
 * numeric ControlPlane keys, values as numeric STRINGS; `maxRpm` must be >= 1
 * and is clamped server-side (`ANHUR_AGENT_PROFILE_MAX_RPM`, default 1200) —
 * read the effective value from `ClientTenant.max_rpm`.
 */
export interface AgentProfile {
  config?: Record<string, string>;
  maxRpm?: number;
}

/** Body of `createTenant` / `createChildTenant`. */
export interface CreateTenantRequest {
  /** Required; "shared" (any case) is refused by the server. */
  tenantId: string;
  description?: string;
  /** Omitted → no "profile" key; `{}` → `"profile":{}` (valid, applies nothing). */
  profile?: AgentProfile;
}

/** Answer of tenant creation (`tenantCreatedBody`,
 * `server/handler/client_tenants.go`). No credential is returned. `client_id`
 * is the OWNER (the caller, or the child); `max_rpm` is the effective clamped
 * value, absent when none was sent. */
export interface ClientTenant {
  client_id: string;
  tenant_id: string;
  composite_tenant: string;
  created: boolean;
  max_rpm?: number;
}

/** The token to mint. */
export interface ImpersonateRequest {
  /** Required; "", "*" and "shared" are refused. */
  tenantId: string;
  /** Optional on the plain route (= the client key's ceiling); required on the child route. */
  scope?: KeyScope;
  /** Restrict the token to L1 (chat) planes; sent only when true. */
  l1Only?: boolean;
  description?: string;
  /** Lifetime in whole seconds; omitted/0 → server default 3600 s (capped at 24 h). */
  ttlSeconds?: number;
}

/**
 * A minted token (`impersonateForClient`, `server/handler/client_impersonate.go`).
 *
 * Callers MUST rely on `scope` and `l1_only` as ECHOED here: the server lowers
 * a scope above the client key's ceiling. The SDK already refuses an echo
 * weaker than the request (`AnhurClientAPIResponseError`). `api_key` is the
 * secret — never log it.
 */
export interface ImpersonatedKey {
  api_key: string;
  key_id: string;
  client_id: string;
  tenant_id: string;
  composite_tenant: string;
  kind: string;
  scope: string;
  l1_only: boolean;
  expires_in: number;
  expires_at: Date;
}

/** Answer of `revokeImpersonation` (`server/handler/client_keys.go`). */
export interface RevokeResult {
  revoked: boolean;
  key_id: string;
}

/** Constructor options of `ClientAPI`. */
export interface ClientAPIOptions {
  /** A CLIENT key (kind=client), never a tenant key. */
  clientKey: string;
  /** Server URL. Defaults to `https://anhurdb.yoven.ai`. */
  url?: string;
  /** Per-request timeout in milliseconds (default 30 000, like Go and Python). */
  timeoutMs?: number;
}
