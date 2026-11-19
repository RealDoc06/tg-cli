#!/usr/bin/env bash
# Builds the release binaries for every supported platform into dist/, with a
# checksums.txt that install.sh and `tg update` verify downloads against.
#
# Usage: scripts/build-release.sh <version>      (for example v1.2.3)
#
# TG_API_ID and TG_API_HASH, when set, are baked in as the default Telegram
# API credentials so users of the build do not have to register their own.
set -euo pipefail

version=${1:?usage: build-release.sh <version>}
cd "$(dirname "$0")/.."
rm -rf dist
mkdir dist

config=github.com/RealDoc06/tg-cli/internal/config
ldflags="-s -w -X main.version=$version"
if [ -n "${TG_API_ID:-}" ] && [ -n "${TG_API_HASH:-}" ]; then
  ldflags="$ldflags -X $config.DefaultAPIID=$TG_API_ID -X $config.DefaultAPIHash=$TG_API_HASH"
fi

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*} arch=${target#*/}
  out=dist/tg_${os}_${arch}
  [ "$os" = windows ] && out=$out.exe
  echo "building $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/tg
done

(cd dist && sha256sum tg_* > checksums.txt)
