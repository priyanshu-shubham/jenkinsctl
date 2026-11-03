#!/usr/bin/env bash
# Builds release archives into dist/. Used by the release workflow; run locally
# to reproduce a release: scripts/build-release.sh v1.2.3
set -euo pipefail

version="${1:?usage: build-release.sh VERSION}"
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

cd "$(dirname "${BASH_SOURCE[0]}")/.."
rm -rf build dist
mkdir -p build dist

for target in $targets; do
	os="${target%/*}" arch="${target#*/}"
	name="jenkinsctl_${os}_${arch}"
	ext=""
	[[ $os == windows ]] && ext=".exe"
	mkdir -p "build/$name"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
		-ldflags "-s -w -X main.version=$version" \
		-o "build/$name/jenkinsctl$ext" ./cmd/jenkinsctl
	cp skills/jenkinsctl/SKILL.md README.md "build/$name/"
	if [[ $os == windows ]]; then
		if command -v zip >/dev/null; then
			(cd build && zip -qr "../dist/$name.zip" "$name")
		else
			(cd build && python3 -m zipfile -c "../dist/$name.zip" "$name")
		fi
	else
		tar -C build -czf "dist/$name.tar.gz" "$name"
	fi
done

(cd dist && sha256sum -- * >checksums.txt)
rm -rf build
ls -1 dist
