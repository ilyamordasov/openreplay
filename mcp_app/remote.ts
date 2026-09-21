import { randomUUID, timingSafeEqual } from "node:crypto";
import http, { type IncomingMessage, type ServerResponse } from "node:http";

import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";

import { createOpenReplayMcpServer } from "./lib/createServer.js";
import { abortAllPolls, loadPersistedState } from "./lib/state.js";

const PORT = Number.parseInt(process.env.MCP_PORT || "3000", 10);
const ENDPOINT = process.env.MCP_ENDPOINT_PATH || "/mcp";
const HEALTH_PATH = process.env.MCP_HEALTH_PATH || "/healthz";
const ACCESS_TOKEN = process.env.MCP_ACCESS_TOKEN || "";
const MAX_BODY_BYTES = Number.parseInt(process.env.MCP_MAX_BODY_BYTES || "1048576", 10);
const MAX_SESSIONS = Number.parseInt(process.env.MCP_MAX_SESSIONS || "32", 10);
const SESSION_TTL_MS = Number.parseInt(process.env.MCP_SESSION_TTL_MS || "3600000", 10);
const SOURCE_SHA = process.env.MCP_SOURCE_SHA || "unknown";

if (!Number.isFinite(PORT) || PORT <= 0 || PORT > 65535) {
  throw new Error("MCP_PORT must be a valid TCP port");
}
if (!ENDPOINT.startsWith("/") || ENDPOINT.includes("?") || ENDPOINT.includes("#")) {
  throw new Error("MCP_ENDPOINT_PATH must be an absolute URL path");
}
if (ACCESS_TOKEN.length < 32) {
  throw new Error("MCP_ACCESS_TOKEN must be set to at least 32 characters");
}

type SessionEntry = {
  transport: StreamableHTTPServerTransport;
  server: McpServer;
  lastSeen: number;
};

const sessions = new Map<string, SessionEntry>();

function sendJson(res: ServerResponse, status: number, body: unknown) {
  if (res.headersSent) return;
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json; charset=utf-8");
  res.setHeader("Cache-Control", "no-store");
  res.end(JSON.stringify(body));
}

function bearerIsValid(req: IncomingMessage): boolean {
  const header = req.headers.authorization;
  if (!header || !header.startsWith("Bearer ")) return false;

  const actual = Buffer.from(header.slice("Bearer ".length), "utf8");
  const expected = Buffer.from(ACCESS_TOKEN, "utf8");
  if (actual.length !== expected.length) return false;
  return timingSafeEqual(actual, expected);
}

async function readJsonBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let total = 0;

  for await (const chunk of req) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    total += buffer.length;
    if (total > MAX_BODY_BYTES) {
      throw Object.assign(new Error("request body too large"), { statusCode: 413 });
    }
    chunks.push(buffer);
  }

  if (chunks.length === 0) return undefined;

  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw Object.assign(new Error("invalid JSON body"), { statusCode: 400 });
  }
}

function getSessionId(req: IncomingMessage): string | undefined {
  const value = req.headers["mcp-session-id"];
  return Array.isArray(value) ? value[0] : value;
}

async function closeEntry(sessionId: string, entry: SessionEntry) {
  sessions.delete(sessionId);
  try {
    await entry.transport.close();
  } catch (error) {
    console.error(`[REMOTE] failed to close transport ${sessionId}:`, error);
  }
  try {
    await entry.server.close();
  } catch (error) {
    console.error(`[REMOTE] failed to close server ${sessionId}:`, error);
  }
}

async function createSession(
  req: IncomingMessage,
  res: ServerResponse,
  body: unknown,
) {
  if (sessions.size >= MAX_SESSIONS) {
    sendJson(res, 429, {
      jsonrpc: "2.0",
      error: { code: -32000, message: "Too many active MCP sessions" },
      id: null,
    });
    return;
  }

  let entry: SessionEntry | undefined;
  const transport = new StreamableHTTPServerTransport({
    sessionIdGenerator: () => randomUUID(),
    onsessioninitialized: (sessionId) => {
      if (!entry) return;
      sessions.set(sessionId, entry);
      console.error(`[REMOTE] session initialized: ${sessionId}`);
    },
  });
  const mcpServer = createOpenReplayMcpServer();
  entry = { transport, server: mcpServer, lastSeen: Date.now() };

  transport.onclose = () => {
    const sessionId = transport.sessionId;
    if (sessionId) {
      sessions.delete(sessionId);
      console.error(`[REMOTE] session closed: ${sessionId}`);
    }
  };

  await mcpServer.connect(transport);
  await transport.handleRequest(req, res, body);
}

async function handleMcp(req: IncomingMessage, res: ServerResponse) {
  if (!bearerIsValid(req)) {
    res.setHeader("WWW-Authenticate", 'Bearer realm="openreplay-mcp"');
    sendJson(res, 401, { error: "unauthorized" });
    return;
  }

  const sessionId = getSessionId(req);
  const entry = sessionId ? sessions.get(sessionId) : undefined;

  if (req.method === "POST") {
    let body: unknown;
    try {
      body = await readJsonBody(req);
    } catch (error: any) {
      sendJson(res, error?.statusCode || 400, { error: error?.message || "bad request" });
      return;
    }

    if (entry) {
      entry.lastSeen = Date.now();
      await entry.transport.handleRequest(req, res, body);
      return;
    }

    if (!sessionId && isInitializeRequest(body)) {
      await createSession(req, res, body);
      return;
    }

    sendJson(res, 400, {
      jsonrpc: "2.0",
      error: { code: -32000, message: "Invalid or missing MCP session ID" },
      id: null,
    });
    return;
  }

  if (req.method === "GET" || req.method === "DELETE") {
    if (!sessionId || !entry) {
      sendJson(res, 400, { error: "Invalid or missing MCP session ID" });
      return;
    }
    entry.lastSeen = Date.now();
    await entry.transport.handleRequest(req, res);
    return;
  }

  res.setHeader("Allow", "GET, POST, DELETE");
  sendJson(res, 405, { error: "method not allowed" });
}

const httpServer = http.createServer(async (req, res) => {
  res.setHeader("X-Content-Type-Options", "nosniff");

  try {
    const url = new URL(req.url || "/", "http://localhost");

    if (url.pathname === HEALTH_PATH) {
      sendJson(res, 200, {
        status: "ok",
        transport: "streamable-http",
        sourceSha: SOURCE_SHA,
        activeSessions: sessions.size,
      });
      return;
    }

    if (url.pathname !== ENDPOINT) {
      sendJson(res, 404, { error: "not found" });
      return;
    }

    await handleMcp(req, res);
  } catch (error) {
    console.error("[REMOTE] request failed:", error);
    sendJson(res, 500, {
      jsonrpc: "2.0",
      error: { code: -32603, message: "Internal server error" },
      id: null,
    });
  }
});

httpServer.requestTimeout = 300_000;
httpServer.headersTimeout = 60_000;
httpServer.keepAliveTimeout = 75_000;

const cleanupTimer = setInterval(() => {
  const now = Date.now();
  for (const [sessionId, entry] of sessions) {
    if (now - entry.lastSeen > SESSION_TTL_MS) {
      console.error(`[REMOTE] expiring idle session: ${sessionId}`);
      void closeEntry(sessionId, entry);
    }
  }
}, Math.min(60_000, SESSION_TTL_MS));
cleanupTimer.unref();

async function shutdown(signal: string) {
  console.error(`[REMOTE] received ${signal}, shutting down`);
  clearInterval(cleanupTimer);
  abortAllPolls();

  for (const [sessionId, entry] of [...sessions]) {
    await closeEntry(sessionId, entry);
  }

  await new Promise<void>((resolve) => httpServer.close(() => resolve()));
}

async function main() {
  await loadPersistedState();

  httpServer.listen(PORT, "0.0.0.0", () => {
    console.error(
      `[REMOTE] OpenReplay MCP Streamable HTTP READY on 0.0.0.0:${PORT}${ENDPOINT}`,
    );
  });
}

for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, () => {
    void shutdown(signal)
      .catch((error) => console.error("[REMOTE] shutdown failed:", error))
      .finally(() => process.exit(0));
  });
}

main().catch((error) => {
  console.error("[REMOTE] fatal error:", error);
  process.exit(1);
});
