import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";

import { createOpenReplayMcpServer } from "./lib/createServer.js";
import { loadPersistedState, abortAllPolls } from "./lib/state.js";

async function main() {
  console.error("[SERVER] Starting OpenReplay MCP server on stdio");
  await loadPersistedState();

  const server = createOpenReplayMcpServer();
  const transport = new StdioServerTransport();
  await server.connect(transport);

  console.error("[SERVER] OpenReplay MCP Server READY on stdio");
}

function shutdown() {
  console.error("[SERVER] Shutting down, aborting polls...");
  abortAllPolls();
  process.exit(0);
}

process.stdin.on("end", shutdown);
process.stdin.on("close", shutdown);
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
process.on("exit", () => abortAllPolls());

main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(1);
});
