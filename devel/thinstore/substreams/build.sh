#!/usr/bin/env bash
# Builds the wasm and packs it into thinstore-test.spkg.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
cargo build --target wasm32-unknown-unknown --release
substreams pack -o thinstore-test.spkg substreams.yaml
