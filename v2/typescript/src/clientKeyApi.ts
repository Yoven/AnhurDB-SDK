/**
 * `ClientAPI` — the handle for a CLIENT key on `/api/v1/client/*` (ADR-0034).
 *
 * One domain: the eleven client-key operations (child clients, tenants with an
 * agent profile, scoped/l1_only impersonation, revoke). Routes from
 * `server/router_client.go`; shapes from `server/handler/client_*.go`.
 * Parity: Go `client/client_key*.go`, Python `anhurdb/client/client_key.py`.
 *
 * @example
 * ```ts
 * const api = new ClientAPI({ clientKey, url: "http://localhost:8000" });
 * const child = await api.createChild(accountRef);
 * await api.createChildTenant(child.child_client_id, { tenantId: "geral", profile: { config: { search_top_k: "8" } } });
 * const token = await api.impersonateChild(child.child_client_id, { tenantId: "geral", scope: "write", l1Only: true });
 * ```
 */

import {
  AnhurClientAPIResponseError,
  RESPONSE_CODE_L1_ONLY_NOT_HONOURED,
  RESPONSE_CODE_SCOPE_NOT_HONOURED,
  invalidParam,
  isCanonicalUuid,
  missingField,
  requireCanonicalChildId,
  requireScope,
  requireTenantId,
  unexpectedResponse,
} from "./clientKeyErrors.js";
import { ClientKeyTransport, type JsonObject } from "./clientKeyTransport.js";
import {
  scopeRank,
  type ChildClient,
  type ChildStatus,
  type ClientTenant,
  type CreateTenantRequest,
  type ImpersonateRequest,
  type ImpersonatedKey,
  type RevokeResult,
} from "./clientKeyTypes.js";

const CHILDREN_PATH = "/api/v1/client/children";

/** Read a string field ("" when absent or not a string). */
function stringField(answer: JsonObject, fieldName: string): string {
  const value = answer[fieldName];
  return typeof value === "string" ? value : "";
}

/** Build a ChildClient from one `childSummary` object. */
function childFrom(summary: JsonObject): ChildClient {
  return {
    child_client_id: stringField(summary, "child_client_id"),
    external_ref: stringField(summary, "external_ref"),
    status: stringField(summary, "status"),
    created: summary.created === true,
    created_at: stringField(summary, "created_at"),
  };
}

/** Client-key operations. Every method sends at most ONE request and never
 * retries. Refusals throw `AnhurClientAPIError` (`code` = server `body.error`);
 * inputs that can never succeed throw `AnhurQueryError` (400) before any request. */
export class ClientAPI extends ClientKeyTransport {
  // ── Children ─────────────────────────────────────────────────────────

  /** Create the caller's child for `externalRef` or confirm it. Idempotent per
   * (caller, ref): `created` true once (HTTP 201), then false with the SAME id.
   * Use a stable opaque id per account, never a reusable slug. A new ref beyond
   * the cap is 429 `child_limit`. */
  async createChild(externalRef: string): Promise<ChildClient> {
    if (!(externalRef ?? "").trim()) throw invalidParam("external_ref is required");
    const answer = await this.send("POST", CHILDREN_PATH, { external_ref: externalRef });
    if (!isCanonicalUuid(answer.child_client_id)) {
      throw unexpectedResponse("POST", CHILDREN_PATH, missingField("child_client_id"));
    }
    return childFrom(answer);
  }

  /** The caller's children, ordered by (created_at, id). */
  async listChildren(): Promise<ChildClient[]> {
    const answer = await this.send("GET", CHILDREN_PATH);
    const summaries = answer.children;
    if (!Array.isArray(summaries)) throw unexpectedResponse("GET", CHILDREN_PATH, missingField("children"));
    if (summaries.some((summary) => summary === null || typeof summary !== "object" || Array.isArray(summary))) {
      throw unexpectedResponse("GET", CHILDREN_PATH, "response is not a JSON object");
    }
    return summaries.map((summary) => childFrom(summary as JsonObject));
  }

  /** Status "suspended" (absolute). The child's tokens get 403
   * `child_suspended` within the server's 5 s cache; nothing is revoked. */
  async suspendChild(childId: string): Promise<ChildStatus> {
    return this.setChildStatus(childId, "suspend");
  }

  /** Status "active"; the SAME tokens work again. */
  async resumeChild(childId: string): Promise<ChildStatus> {
    return this.setChildStatus(childId, "resume");
  }

  private async setChildStatus(childId: string, action: string): Promise<ChildStatus> {
    requireCanonicalChildId(childId);
    const path = `${CHILDREN_PATH}/${childId}/${action}`;
    const answer = await this.send("POST", path);
    const childClientId = stringField(answer, "child_client_id");
    const status = stringField(answer, "status");
    if (!childClientId || !status) throw unexpectedResponse("POST", path, missingField("status"));
    return { child_client_id: childClientId, status };
  }

  // ── Tenants ──────────────────────────────────────────────────────────

  /** Tenant ids of the caller's own client (`shared` is never listed). */
  async listTenants(): Promise<string[]> {
    return this.listTenantIds("/api/v1/client/tenants");
  }

  /** Tenant ids a child owns. */
  async listChildTenants(childId: string): Promise<string[]> {
    requireCanonicalChildId(childId);
    return this.listTenantIds(`${CHILDREN_PATH}/${childId}/tenants`);
  }

  private async listTenantIds(path: string): Promise<string[]> {
    const answer = await this.send("GET", path);
    const entries = answer.tenants;
    if (!Array.isArray(entries)) throw unexpectedResponse("GET", path, missingField("tenants"));
    return entries
      .filter((entry): entry is JsonObject => entry !== null && typeof entry === "object")
      .map((entry) => stringField(entry, "tenant_id"));
  }

  /** Create (or confirm) a tenant owned by the caller; the profile is
   * (re)applied either way. */
  async createTenant(request: CreateTenantRequest): Promise<ClientTenant> {
    return this.createTenantAt("/api/v1/client/tenants", request);
  }

  /** Create (or confirm) a tenant owned by the CHILD. */
  async createChildTenant(childId: string, request: CreateTenantRequest): Promise<ClientTenant> {
    requireCanonicalChildId(childId);
    return this.createTenantAt(`${CHILDREN_PATH}/${childId}/tenants`, request);
  }

  private async createTenantAt(path: string, request: CreateTenantRequest): Promise<ClientTenant> {
    requireTenantId(request?.tenantId);
    const maxRpm = request.profile?.maxRpm;
    if (maxRpm !== undefined && (!Number.isInteger(maxRpm) || maxRpm < 1)) {
      throw invalidParam("profile.max_rpm must be >= 1");
    }
    const body: JsonObject = { tenant_id: request.tenantId };
    if (request.description) body.description = request.description;
    if (request.profile !== undefined) {
      const wireProfile: JsonObject = {};
      if (request.profile.config && Object.keys(request.profile.config).length > 0) {
        wireProfile.config = { ...request.profile.config };
      }
      if (maxRpm !== undefined) wireProfile.max_rpm = maxRpm;
      body.profile = wireProfile;
    }
    const answer = await this.send("POST", path, body);
    const tenantId = stringField(answer, "tenant_id");
    const compositeTenant = stringField(answer, "composite_tenant");
    if (!tenantId || !compositeTenant) throw unexpectedResponse("POST", path, missingField("composite_tenant"));
    const tenant: ClientTenant = {
      client_id: stringField(answer, "client_id"),
      tenant_id: tenantId,
      composite_tenant: compositeTenant,
      created: answer.created === true,
    };
    if (Number.isInteger(answer.max_rpm)) tenant.max_rpm = answer.max_rpm as number;
    return tenant;
  }

  // ── Impersonation ────────────────────────────────────────────────────

  /** Mint a token for one of the caller's tenants; no scope = the client
   * key's ceiling. */
  async impersonate(request: ImpersonateRequest): Promise<ImpersonatedKey> {
    requireScope(request?.scope, true);
    return this.impersonateAt("/api/v1/client/impersonate", request);
  }

  /** Mint a token for one of a child's tenants; `scope` is mandatory. */
  async impersonateChild(childId: string, request: ImpersonateRequest): Promise<ImpersonatedKey> {
    requireCanonicalChildId(childId);
    requireScope(request?.scope, false);
    return this.impersonateAt(`${CHILDREN_PATH}/${childId}/impersonate`, request);
  }

  /**
   * Validate, post, and fail closed on an echo weaker than the request.
   *
   * Junior Tip [fail closed on the echo]: a token is a capability. If the
   * caller asked for l1_only or a scope and the answer does not prove it,
   * handing the token over lets an agent act with MORE power than asked —
   * silently. The SDK refuses and returns `keyId` so the caller can revoke. A
   * LOWER scope is accepted (the server lowers to the ceiling by contract).
   */
  private async impersonateAt(path: string, request: ImpersonateRequest): Promise<ImpersonatedKey> {
    requireTenantId(request.tenantId);
    const ttlSeconds = request.ttlSeconds ?? 0;
    if (!Number.isInteger(ttlSeconds) || ttlSeconds < 0) {
      throw invalidParam("ttl must be a whole number of seconds >= 1");
    }
    const body: JsonObject = { tenant_id: request.tenantId };
    if (request.scope) body.scope = request.scope;
    if (request.l1Only) body.l1_only = true;
    if (request.description) body.description = request.description;
    if (ttlSeconds > 0) {
      body.expires_in = ttlSeconds;
      body.ttl_seconds = ttlSeconds;
    }
    const answer = await this.send("POST", path, body);
    const apiKey = stringField(answer, "api_key");
    const keyId = stringField(answer, "key_id");
    if (!apiKey || !keyId) throw unexpectedResponse("POST", path, missingField("api_key"));
    const expiresAt = new Date(stringField(answer, "expires_at"));
    if (Number.isNaN(expiresAt.getTime())) throw unexpectedResponse("POST", path, missingField("expires_at"));
    const echoedL1Only = answer.l1_only === true;
    const echoedScope = stringField(answer, "scope");
    if (request.l1Only && !echoedL1Only) {
      throw new AnhurClientAPIResponseError("POST", path, RESPONSE_CODE_L1_ONLY_NOT_HONOURED,
        `l1_only was requested but the response echoes l1_only=false (key_id=${keyId})`, keyId);
    }
    if (request.scope && (scopeRank(echoedScope) === 0 || scopeRank(echoedScope) > scopeRank(request.scope))) {
      throw new AnhurClientAPIResponseError("POST", path, RESPONSE_CODE_SCOPE_NOT_HONOURED,
        `scope "${request.scope}" was requested but the response echoes "${echoedScope}" (key_id=${keyId})`, keyId);
    }
    return {
      api_key: apiKey,
      key_id: keyId,
      client_id: stringField(answer, "client_id"),
      tenant_id: stringField(answer, "tenant_id"),
      composite_tenant: stringField(answer, "composite_tenant"),
      kind: stringField(answer, "kind"),
      scope: echoedScope,
      l1_only: echoedL1Only,
      expires_in: Number.isInteger(answer.expires_in) ? (answer.expires_in as number) : 0,
      expires_at: expiresAt,
    };
  }

  /** Revoke a token minted by the caller's client or one of its children.
   * 404 `key_not_found`, 403 `key_not_owned`. */
  async revokeImpersonation(keyId: string): Promise<RevokeResult> {
    if (!(keyId ?? "").trim()) throw invalidParam("key_id is required");
    const path = "/api/v1/client/impersonate/revoke";
    const answer = await this.send("POST", path, { key_id: keyId });
    if (answer.revoked !== true) throw unexpectedResponse("POST", path, missingField("revoked"));
    return { revoked: true, key_id: stringField(answer, "key_id") };
  }
}
