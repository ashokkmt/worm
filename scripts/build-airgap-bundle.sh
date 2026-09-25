#!/bin/sh
set -eu
version=${1:-dev}
out=${2:-dist}
go mod vendor
mkdir -p "$out"
docker build -t "worm:$version" .
docker build -f docker/sim/Dockerfile -t "worm-logsim:$version" .
docker save "worm:$version" "worm-logsim:$version" -o "$out/worm-images-$version.tar"
cp docker-compose.yaml "$out/docker-compose.yaml"
cp -R packs schemas sources sinks "$out/"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$out" && sha256sum "worm-images-$version.tar" docker-compose.yaml > SHA256SUMS)
else
	(cd "$out" && shasum -a 256 "worm-images-$version.tar" docker-compose.yaml > SHA256SUMS)
fi
tar -C "$out" -czf "$out/worm-airgap-$version.tar.gz" "worm-images-$version.tar" docker-compose.yaml packs schemas sources sinks SHA256SUMS
