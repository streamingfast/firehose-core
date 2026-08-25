#!/usr/bin/env bash
# Starts substreams-tier1 (:10016) and tier2 (:10017) over the merged blocks in
# ./firehose-data, without any live component. Logs go to logs/offline.log.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"
mkdir -p logs
exec ../firecore -c offline.yaml start "$@" 2>&1 | tee logs/offline.log
