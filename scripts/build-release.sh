#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
output_arg="${2:-}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'Usage: %s vMAJOR.MINOR.PATCH [output-directory]\n' "$0" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
output="${output_arg:-dist/$tag}"
mkdir -p "$output"
output="$(cd "$output" && pwd)"

if [[ "${WORM_OFFLINE:-0}" == "1" ]]; then
  npm ci --offline --prefix web
  export GOPROXY=off
else
  npm ci --prefix web
fi
npm run build --prefix web
go vet ./...
go test -count=1 ./...

tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
archives=()

for target in "${targets[@]}"; do
  IFS=/ read -r goos goarch <<< "$target"
  stage="$tmp_root/$goos-$goarch"
  mkdir -p "$stage"
  binary="worm"
  [[ "$goos" == windows ]] && binary="worm.exe"

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w -X 'main.Version=${tag}'" \
    -o "$stage/$binary" ./cmd/worm
  cp -R packs schemas sources sinks "$stage/"

  archive="worm-${tag}-${goos}-${goarch}.tar.gz"
  tar -czf "$output/$archive" -C "$stage" .
  archives+=("$archive")
  printf 'Built %s\n' "$output/$archive"
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$output" && sha256sum "${archives[@]}" > SHA256SUMS)
else
  (cd "$output" && shasum -a 256 "${archives[@]}" > SHA256SUMS)
fi
printf 'Checksums: %s/SHA256SUMS\n' "$output"
