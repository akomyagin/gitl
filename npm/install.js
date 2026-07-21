// gitl-cli npm installer: downloads the prebuilt `gitl` binary for this
// platform from GitHub Releases and verifies its SHA256 checksum against the
// release's checksums.txt before extracting it into npm/bin/.
//
// Runs as the package's postinstall script; the bin/gitl.js shim also
// requires ensureBinary() from here, so an install done with --ignore-scripts
// still self-heals lazily on first run.
//
// Zero external dependencies by design: only Node's standard library.
'use strict';

const crypto = require('crypto');
const fs = require('fs');
const https = require('https');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');

const REPO = 'akomyagin/gitl';

// VERSION_PLACEHOLDER is what package.json carries in the repo checkout; the
// release workflow stamps the real tag version before `npm publish`. Seeing it
// at install time means a dev checkout, where there is no release to download.
const VERSION_PLACEHOLDER = '0.0.0-dev';

// binaryPath returns where the downloaded binary lives (or will live).
function binaryPath() {
  const exe = process.platform === 'win32' ? 'gitl.exe' : 'gitl';
  return path.join(__dirname, 'bin', exe);
}

// isValidBinary reports whether the binary at p looks installed: it must exist
// and be non-empty. An interrupted install (killed process, full disk) can
// leave a truncated/zero-size file behind — treating it as "not installed"
// lets ensureBinary re-download it, keeping the lazy self-heal promise. No
// full checksum here by design: that would cost a network round-trip on every
// CLI invocation; this only guards against an obviously broken empty file.
function isValidBinary(p) {
  try {
    return fs.statSync(p).size > 0;
  } catch {
    return false; // missing (or unreadable) — not installed
  }
}

// assetName maps the Node platform/arch to the goreleaser archive name
// (name_template in .goreleaser.yaml: gitl_<version>_<os>_<arch>, tar.gz
// everywhere except a zip override for windows).
function assetName(version) {
  const goos = { linux: 'linux', darwin: 'darwin', win32: 'windows' }[process.platform];
  const goarch = { x64: 'amd64', arm64: 'arm64' }[process.arch];
  if (!goos || !goarch) {
    throw new Error(
      `gitl-cli: no prebuilt gitl binary for ${process.platform}/${process.arch}; ` +
      `build from source: https://github.com/${REPO}`
    );
  }
  const ext = goos === 'windows' ? 'zip' : 'tar.gz';
  return `gitl_${version}_${goos}_${goarch}.${ext}`;
}

// download GETs a URL (following redirects — GitHub release assets redirect to
// a CDN) and resolves with the response body as a Buffer.
function download(url, redirectsLeft = 5) {
  return new Promise((resolve, reject) => {
    const req = https.get(url, { headers: { 'User-Agent': 'gitl-cli-npm-installer' } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        res.resume();
        if (redirectsLeft <= 0) return reject(new Error(`too many redirects fetching ${url}`));
        return resolve(download(res.headers.location, redirectsLeft - 1));
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`GET ${url}: HTTP ${res.statusCode}`));
      }
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve(Buffer.concat(chunks)));
      res.on('error', reject);
    });
    req.on('error', reject);
  });
}

// verifyChecksum checks the archive against its checksums.txt entry
// ("<hex>  <asset>" per line) and throws on any mismatch or missing entry.
function verifyChecksum(archive, asset, sumsText) {
  let expected = null;
  for (const line of sumsText.split('\n')) {
    const fields = line.trim().split(/\s+/);
    if (fields.length === 2 && fields[1] === asset) {
      expected = fields[0].toLowerCase();
      break;
    }
  }
  if (!expected) {
    throw new Error(`checksums.txt has no entry for ${asset}`);
  }
  const actual = crypto.createHash('sha256').update(archive).digest('hex');
  if (actual !== expected) {
    throw new Error(`checksum mismatch for ${asset}: expected ${expected}, got ${actual}`);
  }
}

// extract unpacks the verified archive into npm/bin/. It shells out to the
// system `tar` rather than hand-rolling a tar/zip parser: with zero allowed
// npm dependencies a correct parser is the riskier path, while `tar` ships on
// every platform this package supports — Linux, macOS, and Windows 10+ (whose
// bundled bsdtar extracts .zip archives too, covering the windows asset).
function extract(archive, asset) {
  const binDir = path.join(__dirname, 'bin');
  fs.mkdirSync(binDir, { recursive: true });
  const tmp = path.join(os.tmpdir(), `gitl-cli-${process.pid}-${asset}`);
  fs.writeFileSync(tmp, archive);
  try {
    execFileSync('tar', ['-xf', tmp, '-C', binDir], { stdio: 'inherit' });
  } finally {
    fs.rmSync(tmp, { force: true });
  }
}

// ensureBinary downloads, verifies, and extracts the binary if it is not
// already present. Returns the binary path. Exported for the bin shim's lazy
// path (installs done with --ignore-scripts).
async function ensureBinary() {
  const bin = binaryPath();
  if (isValidBinary(bin)) return bin;

  const version = require('./package.json').version;
  if (version === VERSION_PLACEHOLDER) {
    throw new Error(
      'gitl-cli: package.json still carries the dev placeholder version — ' +
      'this is a repo checkout, not a published package; build gitl from source instead'
    );
  }

  const asset = assetName(version);
  const base = `https://github.com/${REPO}/releases/download/v${version}`;
  console.log(`gitl-cli: downloading ${asset} ...`);
  const [archive, sums] = await Promise.all([
    download(`${base}/${asset}`),
    download(`${base}/checksums.txt`),
  ]);
  verifyChecksum(archive, asset, sums.toString('utf8'));
  extract(archive, asset);

  if (!fs.existsSync(bin)) {
    throw new Error(`archive ${asset} did not contain ${path.basename(bin)}`);
  }
  if (process.platform !== 'win32') {
    fs.chmodSync(bin, 0o755);
  }
  console.log(`gitl-cli: installed ${bin}`);
  return bin;
}

module.exports = { ensureBinary, binaryPath, isValidBinary };

if (require.main === module) {
  // postinstall entry point. In a dev checkout (placeholder version) there is
  // nothing to download — skip quietly instead of failing `npm install` on the
  // repo itself; the real version is stamped in CI before publishing.
  const version = require('./package.json').version;
  if (version === VERSION_PLACEHOLDER) {
    console.log('gitl-cli: dev checkout (placeholder version), skipping binary download');
    process.exit(0);
  }
  ensureBinary().catch((err) => {
    console.error(String(err.message || err));
    process.exit(1);
  });
}
