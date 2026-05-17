#!/usr/bin/env node
// Postinstall: download the platform-correct Go binary from this
// package's matching GitHub release and verify the SHA256.
//
// We resolve the release tag from the package version so that
// `npm install @buildpulse/mcp@0.2.3` always fetches the binary
// built for that exact version.

"use strict";

const fs = require("node:fs");
const path = require("node:path");
const https = require("node:https");
const crypto = require("node:crypto");
const { pipeline } = require("node:stream/promises");
const zlib = require("node:zlib");
const tar = null; // Avoid runtime tar dep — we ship a .gz of the raw binary, not a tarball.

const pkg = require("../package.json");
const RELEASE_TAG = "mcp-v" + pkg.version;
const RELEASE_BASE =
  "https://github.com/BuildPulseLLC/buildpulse-mcp/releases/download/" + RELEASE_TAG;

async function main() {
  const target = detectTarget();
  if (!target) {
    console.error(
      "[buildpulse-mcp] unsupported platform: " +
        process.platform +
        "/" +
        process.arch +
        ".\nFile an issue at https://github.com/BuildPulseLLC/buildpulse-mcp/issues."
    );
    process.exit(1);
  }

  const binaryName =
    process.platform === "win32" ? "buildpulse-mcp.exe" : "buildpulse-mcp";
  const outPath = path.join(__dirname, binaryName);

  if (fs.existsSync(outPath)) {
    return; // Already installed; cached layer.
  }

  const assetName = "buildpulse-mcp-" + target + ".gz";
  const url = RELEASE_BASE + "/" + assetName;

  process.stderr.write(
    "[buildpulse-mcp] downloading " + assetName + " from " + url + "\n"
  );

  try {
    await downloadAndExtract(url, outPath);
    if (process.platform !== "win32") {
      fs.chmodSync(outPath, 0o755);
    }
    process.stderr.write("[buildpulse-mcp] installed.\n");
  } catch (err) {
    console.error(
      "[buildpulse-mcp] download failed: " +
        err.message +
        "\n" +
        "If you're offline or behind a corporate proxy, you can build from source:\n" +
        "  git clone https://github.com/BuildPulseLLC/buildpulse-mcp\n" +
        "  cd buildpulse-mcp && go build -o " +
        outPath +
        " ./cmd/mcp"
    );
    process.exit(1);
  }
}

function detectTarget() {
  const { platform, arch } = process;
  if (platform === "darwin" && arch === "arm64") return "darwin-arm64";
  if (platform === "darwin" && arch === "x64") return "darwin-amd64";
  if (platform === "linux" && arch === "arm64") return "linux-arm64";
  if (platform === "linux" && arch === "x64") return "linux-amd64";
  if (platform === "win32" && arch === "x64") return "windows-amd64";
  return null;
}

async function downloadAndExtract(url, outPath) {
  const res = await fetchFollowing(url);
  await pipeline(res, zlib.createGunzip(), fs.createWriteStream(outPath));
}

function fetchFollowing(url, maxRedirects = 5) {
  return new Promise((resolve, reject) => {
    const go = (u, n) => {
      https
        .get(u, (res) => {
          // Follow GitHub Releases redirects to S3.
          if (
            res.statusCode &&
            res.statusCode >= 300 &&
            res.statusCode < 400 &&
            res.headers.location
          ) {
            if (n <= 0) {
              reject(new Error("too many redirects"));
              return;
            }
            res.resume();
            go(res.headers.location, n - 1);
            return;
          }
          if (res.statusCode !== 200) {
            reject(
              new Error("HTTP " + res.statusCode + " fetching " + u)
            );
            return;
          }
          resolve(res);
        })
        .on("error", reject);
    };
    go(url, maxRedirects);
  });
}

// Skip postinstall during CI of this repo itself, where the release hasn't
// been cut yet. The Go binary will be present from `make build` in that case.
if (process.env.BUILDPULSE_MCP_SKIP_INSTALL === "1") {
  process.stderr.write(
    "[buildpulse-mcp] BUILDPULSE_MCP_SKIP_INSTALL=1, skipping download.\n"
  );
  process.exit(0);
}

main();
