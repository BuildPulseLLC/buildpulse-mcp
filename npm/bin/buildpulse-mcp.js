#!/usr/bin/env node
// BuildPulse MCP server entry point.
//
// This is a thin Node shim that execs the platform-native Go binary
// downloaded by lib/install.js at install time. We use Node only for
// the npm distribution surface — the actual MCP server is Go.

const { spawn } = require("node:child_process");
const path = require("node:path");
const fs = require("node:fs");

const binaryPath = path.join(__dirname, "..", "lib", binaryName());

if (!fs.existsSync(binaryPath)) {
  console.error(
    "[buildpulse-mcp] native binary not found at " + binaryPath + ".\n" +
    "Try reinstalling: `npm install @buildpulse/mcp` or `npx -y @buildpulse/mcp`."
  );
  process.exit(1);
}

// Inherit stdio so the Go binary speaks MCP directly over our stdin/stdout.
// stderr is forwarded as-is — Claude Desktop / Cursor surface it as
// "server logs" in the integration's debug panel.
const child = spawn(binaryPath, process.argv.slice(2), {
  stdio: "inherit",
  env: process.env,
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
  } else {
    process.exit(code ?? 0);
  }
});

child.on("error", (err) => {
  console.error("[buildpulse-mcp] failed to spawn native binary:", err.message);
  process.exit(1);
});

function binaryName() {
  return process.platform === "win32" ? "buildpulse-mcp.exe" : "buildpulse-mcp";
}
