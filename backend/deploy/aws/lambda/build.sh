#!/bin/sh
# Builds the Lambda bootstrap (arm64, provided.al2023) into build/.
# Then: sam deploy --guided   (first time; afterwards: sam deploy)
set -eu
cd "$(dirname "$0")"
VERSION=${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}
mkdir -p build
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o build/bootstrap ../../../cmd/openpersona-lambda
ls -l build/bootstrap
