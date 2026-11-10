#!/bin/sh
# Build and run the Mapper server from ./cmd/mapper.
# Usage: ./run.sh [mapper args...]
# Env: MAPPER_BIN overrides the output binary path (default ./bin/mapper).
set -eu

cd "$(dirname "$0")"

BIN="${MAPPER_BIN:-./bin/mapper}"
mkdir -p "$(dirname "$BIN")"
go build -o "$BIN" ./cmd/mapper
exec "$BIN" "$@"
