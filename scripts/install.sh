#!/bin/sh
# Install the CLI from a published release. No server or account is configured.
set -eu
install_version=0.2.0
install_prefix="$HOME/.local/bin"
install_base=https://github.com/samekind/soseek/releases/download
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version) [ "$#" -ge 2 ] || exit 2; install_version=$2; shift 2;;
        --prefix) [ "$#" -ge 2 ] || exit 2; install_prefix=$2; shift 2;;
        --base-url) [ "$#" -ge 2 ] || exit 2; install_base=$2; shift 2;;
        --help|-h) printf '%s\n' 'Usage: install.sh [--version 0.2.0] [--prefix ~/.local/bin] [--base-url HTTPS_RELEASE_DOWNLOAD_URL]'; exit 0;;
        *) printf '%s\n' "Unknown option: $1" >&2; exit 2;;
    esac
done
case "$install_version" in *[!A-Za-z0-9._-]*|'') echo 'Invalid version' >&2; exit 2;; esac
case "$install_base" in https://*) ;; *) echo 'Release base URL must use HTTPS' >&2; exit 2;; esac
case "$(uname -s)" in Darwin) install_os=darwin;; Linux) install_os=linux;; *) echo 'Use the release ZIP on Windows.' >&2; exit 2;; esac
case "$(uname -m)" in arm64|aarch64) install_arch=arm64;; x86_64|amd64) install_arch=amd64;; *) echo 'Unsupported CPU architecture' >&2; exit 2;; esac
install_name="soseek_${install_version}_${install_os}_${install_arch}.tar.gz"
install_tmp=$(mktemp -d "${TMPDIR:-/tmp}/soseek-install.XXXXXX")
trap 'rm -rf "$install_tmp"' EXIT HUP INT TERM
install_url="${install_base%/}/v${install_version}"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$install_url/$install_name" -o "$install_tmp/$install_name"
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$install_url/SHA256SUMS-$install_version" -o "$install_tmp/checksums"
install_expected=$(awk -v name="$install_name" '$2 == name { print $1 }' "$install_tmp/checksums")
[ "${#install_expected}" -eq 64 ] || { echo 'Missing or ambiguous checksum' >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
    install_actual=$(sha256sum "$install_tmp/$install_name" | awk '{print $1}')
else
    install_actual=$(shasum -a 256 "$install_tmp/$install_name" | awk '{print $1}')
fi
[ "$install_actual" = "$install_expected" ] || { echo 'Checksum verification failed' >&2; exit 1; }
# Extract only the expected CLI, not arbitrary archive paths or server files.
tar -xzf "$install_tmp/$install_name" -C "$install_tmp" ./soseek
mkdir -p "$install_prefix"
install -m 0755 "$install_tmp/soseek" "$install_prefix/soseek"
printf '%s\n' "Installed $install_prefix/soseek" 'Next: soseek auth login --server https://YOUR_SERVER --token-file YOUR_PRIVATE_TOKEN_FILE'
