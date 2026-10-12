#!/bin/sh
# Installs the latest peaka release for this machine:
#   curl -fsSL https://raw.githubusercontent.com/peakacom/peaka-cli/main/install.sh | sh
# Options through the environment:
#   PEAKA_VERSION=v1.2.3   a specific release (default: latest)
#   PEAKA_INSTALL_DIR=…    where the binary goes (default: /usr/local/bin, or ~/.local/bin when not writable)
#   GITHUB_TOKEN=…         for a private repository
set -eu

REPO="peakacom/peaka-cli"
VERSION="${PEAKA_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "peaka: no build for $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin|linux) ;;
  *) echo "peaka: use the Windows archive from https://github.com/$REPO/releases" >&2; exit 1 ;;
esac

auth=""
if [ -n "${GITHUB_TOKEN:-}" ]; then auth="-H \"Authorization: Bearer $GITHUB_TOKEN\""; fi
api() { eval curl -fsSL $auth -H '"Accept: application/vnd.github+json"' "\"$1\""; }

if [ "$VERSION" = latest ]; then
  tag=$(api "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
else
  tag="$VERSION"
fi
[ -n "$tag" ] || { echo "peaka: could not find a release" >&2; exit 1; }
version="${tag#v}"
name="peaka_${version}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "peaka $tag ($os/$arch)" >&2
if [ -n "${GITHUB_TOKEN:-}" ]; then
  # a private repository: assets are fetched by id through the API
  assets=$(api "https://api.github.com/repos/$REPO/releases/tags/$tag")
  for f in "$name" checksums.txt; do
    id=$(printf '%s' "$assets" | tr -d '\n ' | grep -o "\"url\":\"[^\"]*/assets/[0-9]*\",\"id\":[0-9]*,\"node_id\":\"[^\"]*\",\"name\":\"$f\"" | head -1 | grep -o 'assets/[0-9]*' | cut -d/ -f2)
    [ -n "$id" ] || { echo "peaka: $f is not on release $tag" >&2; exit 1; }
    curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/octet-stream" \
      "https://api.github.com/repos/$REPO/releases/assets/$id" -o "$tmp/$f"
  done
else
  base="https://github.com/$REPO/releases/download/$tag"
  curl -fsSL "$base/$name" -o "$tmp/$name"
  curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
fi

want=$(grep " $name\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$name" | cut -d' ' -f1)
else got=$(shasum -a 256 "$tmp/$name" | cut -d' ' -f1); fi
[ "$want" = "$got" ] || { echo "peaka: checksum mismatch for $name" >&2; exit 1; }

tar -xzf "$tmp/$name" -C "$tmp" peaka
dir="${PEAKA_INSTALL_DIR:-/usr/local/bin}"
if [ ! -w "$dir" ] && [ -z "${PEAKA_INSTALL_DIR:-}" ]; then dir="$HOME/.local/bin"; fi
mkdir -p "$dir"
install -m 0755 "$tmp/peaka" "$dir/peaka"
echo "installed $dir/peaka" >&2
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "add $dir to your PATH" >&2 ;;
esac
"$dir/peaka" version >&2
