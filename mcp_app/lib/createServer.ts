import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { registerAppResource, RESOURCE_MIME_TYPE } from "@modelcontextprotocol/ext-apps/server";
import fs from "node:fs/promises";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { registerUITools, registerInternalTools } from "./tools.js";

export const OPENREPLAY_RESOURCE_URI = "ui://openreplay/app";

function resolveDistDir(): string {
  const here = import.meta.dirname ?? path.dirname(fileURLToPath(import.meta.url));
  const candidates = [
    process.env.MCP_UI_DIST_DIR,
    path.join(here, "dist"),
    path.join(here, "..", "dist"),
    path.join(process.cwd(), "dist"),
  ].filter((value): value is string => !!value);

  const found = candidates.find((candidate) =>
    existsSync(path.join(candidate, "index.html")),
  );
  if (!found) {
    throw new Error(
      `OpenReplay MCP UI bundle not found. Checked: ${candidates.join(", ")}`,
    );
  }
  return found;
}

export function createOpenReplayMcpServer(): McpServer {
  const distDir = resolveDistDir();
  const server = new McpServer({
    name: "openreplay-mcp-app",
    version: "1.0.0",
  });

  registerUITools(server, OPENREPLAY_RESOURCE_URI);
  registerInternalTools(server);

  registerAppResource(
    server,
    OPENREPLAY_RESOURCE_URI,
    OPENREPLAY_RESOURCE_URI,
    { mimeType: RESOURCE_MIME_TYPE },
    async () => {
      const html = await fs.readFile(path.join(distDir, "index.html"), "utf-8");
      return {
        contents: [
          {
            uri: OPENREPLAY_RESOURCE_URI,
            mimeType: RESOURCE_MIME_TYPE,
            text: html,
            _meta: {
              ui: {
                csp: {
                  connectDomains: ["*.openreplay.com"],
                },
              },
            },
          },
        ],
      };
    },
  );

  return server;
}
