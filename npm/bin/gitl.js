#!/usr/bin/env node
// Thin shim around the real gitl Go binary: makes sure it is present
// (postinstall normally fetched it; installs done with --ignore-scripts get a
// lazy download on first run) and then execs it with stdio inherited — gitl
// streams review tokens to the terminal and detects TTYs, so the pipes must
// pass through untouched.
'use strict';

const { spawn } = require('child_process');
const { ensureBinary, binaryPath, isValidBinary } = require('../install.js');

async function main() {
  let bin = binaryPath();
  if (!isValidBinary(bin)) {
    // Missing OR truncated to zero bytes (interrupted install) — (re)download.
    bin = await ensureBinary();
  }

  const child = spawn(bin, process.argv.slice(2), { stdio: 'inherit' });
  child.on('error', (err) => {
    console.error(`gitl: failed to start ${bin}: ${err.message}`);
    process.exit(1);
  });
  child.on('exit', (code, signal) => {
    if (signal) {
      // Re-raise the signal so callers observe the same termination cause.
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code === null ? 1 : code);
  });
}

main().catch((err) => {
  console.error(`gitl: ${String(err.message || err)}`);
  process.exit(1);
});
