/**
 * An in-process fake of the AnhurDB client-key surface (node:http).
 *
 * Response shapes are copied from the AnhurDB handlers (branch
 * feat/phase1-client-children), not from the SDK's own types, so a passing
 * test proves the SDK reads what the SERVER writes:
 *
 *   POST/GET /children, suspend/resume           server/handler/client_children.go
 *   GET /tenants, GET /children/{id}/tenants      server/handler/client_keys.go (writeTenantList)
 *   POST /tenants, POST /children/{id}/tenants    server/handler/client_tenants.go (tenantCreatedBody)
 *   POST /impersonate, /children/{id}/impersonate server/handler/client_impersonate.go
 *   POST /impersonate/revoke                      server/handler/client_keys.go
 *   error envelope {"error","detail"}             server/handler/response.go (writeErrorCode)
 */

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

/** One request the SDK put on the wire. */
export interface FakeRequest {
  method: string;
  path: string;
  rawBody: string;
  body: Record<string, unknown>;
  apiKey: string | undefined;
  hasTenantHeader: boolean;
}

/** A fixed answer replacing every route. */
export interface StaticAnswer {
  status: number;
  contentType: string;
  body: string;
}

type Json = Record<string, unknown>;

/** Stateful stand-in; `requests` records every request on the wire. */
export class FakeClientServer {
  readonly requests: FakeRequest[] = [];
  impersonateOverride?: (body: Json) => Json;
  staticAnswer?: StaticAnswer;
  baseUrl = "";
  private readonly childByRef = new Map<string, Json>();
  private readonly childStatus = new Map<string, string>();
  private server?: Server;

  async start(): Promise<FakeClientServer> {
    this.server = createServer((request, response) => {
      void this.handle(request, response);
    });
    await new Promise<void>((resolve) => this.server!.listen(0, "127.0.0.1", resolve));
    const address = this.server.address() as AddressInfo;
    this.baseUrl = `http://127.0.0.1:${address.port}`;
    return this;
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve) => this.server?.close(() => resolve()));
  }

  private async handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(chunk as Buffer);
    const rawBody = Buffer.concat(chunks).toString("utf8");
    let body: Json = {};
    try {
      body = rawBody ? (JSON.parse(rawBody) as Json) : {};
    } catch {
      body = {};
    }
    const path = new URL(request.url ?? "/", "http://fake").pathname;
    this.requests.push({
      method: request.method ?? "", path, rawBody, body,
      apiKey: request.headers["x-api-key"] as string | undefined,
      hasTenantHeader: request.headers["x-tenant-id"] !== undefined,
    });
    if (this.staticAnswer) {
      response.writeHead(this.staticAnswer.status, { "Content-Type": this.staticAnswer.contentType });
      response.end(this.staticAnswer.body);
      return;
    }
    const [status, answer] = this.route(request.method ?? "", path, body);
    response.writeHead(status, { "Content-Type": "application/json" });
    response.end(JSON.stringify(answer) + "\n");
  }

  private route(method: string, path: string, body: Json): [number, Json] {
    const base = "/api/v1/client";
    const childMatch = /^\/api\/v1\/client\/children\/([^/]+)\/(suspend|resume|tenants|impersonate)$/.exec(path);
    if (method === "POST" && path === `${base}/children`) return this.createChild(String(body.external_ref));
    if (method === "GET" && path === `${base}/children`) return [200, { children: [...this.childByRef.values()] }];
    if (method === "GET" && (path === `${base}/tenants` || childMatch?.[2] === "tenants")) {
      return [200, { tenants: [{ tenant_id: "contabilidade" }, { tenant_id: "geral" }] }];
    }
    if (method === "POST" && childMatch && (childMatch[2] === "suspend" || childMatch[2] === "resume")) {
      if (!this.childStatus.has(childMatch[1])) {
        return [404, { error: "child_not_found", detail: "no child client with this id for the caller" }];
      }
      const status = childMatch[2] === "suspend" ? "suspended" : "active";
      this.childStatus.set(childMatch[1], status);
      return [200, { child_client_id: childMatch[1], status }];
    }
    const owner = childMatch ? childMatch[1] : "parent-client";
    if (method === "POST" && (path === `${base}/tenants` || childMatch?.[2] === "tenants")) {
      const answer: Json = { client_id: owner, tenant_id: body.tenant_id,
        composite_tenant: `${owner}_${String(body.tenant_id)}`, created: true };
      const maxRpm = (body.profile as Json | undefined)?.max_rpm;
      if (typeof maxRpm === "number") answer.max_rpm = Math.min(maxRpm, 1200);
      return [201, answer];
    }
    if (method === "POST" && (path === `${base}/impersonate` || childMatch?.[2] === "impersonate")) {
      if (this.impersonateOverride) return [201, this.impersonateOverride(body)];
      return [201, {
        api_key: "anhur_secret", key_id: "ik_42", client_id: owner, tenant_id: body.tenant_id,
        composite_tenant: `${owner}_${String(body.tenant_id)}`, scope: body.scope || "admin",
        l1_only: body.l1_only === true, kind: "impersonation", expires_in: 900, expires_at: "2026-11-21T13:00:00Z",
      }];
    }
    if (method === "POST" && path === `${base}/impersonate/revoke`) {
      if (body.key_id !== "ik_42") return [404, { error: "key_not_found", detail: "no impersonation key with this key_id" }];
      return [200, { revoked: true, key_id: body.key_id }];
    }
    return [404, { error: "not found" }];
  }

  private createChild(ref: string): [number, Json] {
    const existing = this.childByRef.get(ref);
    if (existing) {
      const childId = String(existing.child_client_id);
      return [200, { ...existing, status: this.childStatus.get(childId), created: false }];
    }
    const childId = `9b1d2c3e-4f5a-4b6c-8d7e-${String(this.childByRef.size + 1).padStart(12, "0")}`;
    const summary: Json = { child_client_id: childId, external_ref: ref, status: "active", created_at: "2026-11-21T12:00:00Z" };
    this.childByRef.set(ref, summary);
    this.childStatus.set(childId, "active");
    return [201, { ...summary, created: true }];
  }
}

/** Run `body` against a fresh fake, always stopping it. */
export async function withFake(body: (fake: FakeClientServer) => Promise<void>): Promise<void> {
  const fake = await new FakeClientServer().start();
  try {
    await body(fake);
  } finally {
    await fake.stop();
  }
}
