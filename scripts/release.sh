#!/bin/sh
# Build reproducible, dependency-free release binaries. Does not publish.
set -eu

release_version=${1:-0.1.0}
case "$release_version" in *[!A-Za-z0-9._-]*|'') echo 'Invalid version' >&2; exit 2;; esac
release_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
release_out=${SOSEEK_RELEASE_DIR:-"$release_root/dist"}
mkdir -p "$release_out"
release_out=$(CDPATH='' cd -- "$release_out" && pwd)
release_tmp=$(mktemp -d "${TMPDIR:-/tmp}/soseek-release.XXXXXX")
trap 'rm -rf "$release_tmp"' EXIT HUP INT TERM
cd "$release_root"
./scripts/build-web.sh

for release_target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/arm64 windows/amd64; do
    release_os=${release_target%/*}
    release_arch=${release_target#*/}
    release_name="soseek_${release_version}_${release_os}_${release_arch}"
    release_stage="$release_tmp/$release_name"
    mkdir -p "$release_stage"
    release_binary=soseek
    if [ "$release_os" = windows ]; then release_binary=soseek.exe; fi
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build -trimpath -ldflags "-s -w -X main.version=$release_version" -o "$release_stage/$release_binary" ./cmd/soseek
    if [ "$release_os" = linux ]; then
        CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build -trimpath -ldflags "-s -w" -o "$release_stage/soseekd" ./cmd/soseekd
    fi
    if [ -f LICENSE ]; then cp LICENSE "$release_stage/LICENSE"; fi
    if [ "$release_os" = windows ]; then
        (cd "$release_stage" && zip -q "$release_out/$release_name.zip" ./*)
    else
        tar -czf "$release_out/$release_name.tar.gz" -C "$release_stage" .
    fi
    printf '%s\n' "Built $release_name"
done
(
    cd "$release_out"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "soseek_${release_version}_"*.tar.gz "soseek_${release_version}_"*.zip > "SHA256SUMS-$release_version"
    else
        shasum -a 256 "soseek_${release_version}_"*.tar.gz "soseek_${release_version}_"*.zip > "SHA256SUMS-$release_version"
    fi
)
printf '%s\n' "Release artifacts and checksums: $release_out"
