#!/bin/sh
# Build the pinned admin UI and copy it into the Go embed tree. Does not publish.
set -eu

build_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
build_web="$build_root/web"
build_target="$build_root/internal/adminui/dist"

if ! command -v node >/dev/null 2>&1 || ! command -v pnpm >/dev/null 2>&1; then
    echo 'Node.js 24 or newer and pnpm are required to build the admin UI; see docs/ADMIN.md.' >&2
    exit 1
fi
node -e 'if (Number(process.versions.node.split(".")[0]) < 24) { console.error("Admin UI builds require Node.js 24 or newer"); process.exit(1); }'
test -f "$build_web/package.json"
test -f "$build_web/pnpm-lock.yaml"
if [ -L "$build_web/dist" ] || [ -L "$build_target" ]; then
    echo 'Refusing a symlinked web build output directory.' >&2
    exit 1
fi

# Corepack resolves packageManager from the working directory's package.json.
(
    cd "$build_web"
    pnpm install --frozen-lockfile
    pnpm build
)

node --input-type=module - "$build_web/dist" <<'JS'
import fs from 'node:fs';
import path from 'node:path';
const root = path.resolve(process.argv[2]);
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const scripts = [...html.matchAll(/<script\b[^>]*\bsrc\s*=\s*["']([^"']+)["']/gi)].map(match => match[1]);
if (scripts.length === 0) throw Error('Admin build has no JavaScript entry point; refusing a placeholder release');
for (const url of scripts) {
  if (!url.startsWith('/admin/assets/')) throw Error('Admin scripts must use the /admin/ Vite base');
  const target = path.resolve(root, url.slice('/admin/'.length));
  if (!target.startsWith(root + path.sep) || !fs.statSync(target).isFile()) throw Error('Admin entry asset is missing or outside the build directory');
}
JS

mkdir -p "$build_target"
# Only generated files in this repository-owned directory are replaced. The
# tracked placeholder keeps ordinary Go builds usable before a frontend build.
find "$build_target" -mindepth 1 -maxdepth 1 ! -name placeholder.txt -exec rm -rf -- {} +
cp -R "$build_web/dist/." "$build_target/"
test -s "$build_target/index.html"
printf '%s\n' 'Admin UI built and embedded from web/dist (base /admin/).'
