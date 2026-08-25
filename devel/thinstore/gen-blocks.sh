#!/usr/bin/env bash
# Generates merged blocks into ./firehose-data with the dummy blockchain, then stops.
#
#   ./gen-blocks.sh [-c] [blocks]     default: 30500 blocks (-> merged up to ~30400)
#
# -c wipes ./firehose-data first. The offline stack (start-offline.sh) reads what this
# produced, no live component is needed afterwards.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

clean=
while getopts "hc" opt; do
  case $opt in
    c) clean=true;;
    h) sed -n '2,8p' "$0"; exit 0;;
    *) exit 1;;
  esac
done
shift $((OPTIND-1))

export THINSTORE_BLOCKS="${1:-30500}"
target_files=$(( THINSTORE_BLOCKS / 100 - 1 ))
merged="firehose-data/storage/merged-blocks"

if [[ $clean == true ]]; then
  rm -rf firehose-data
fi
mkdir -p logs

merged_count() {
  (ls "$merged" 2>/dev/null || true) | wc -l | tr -d ' '
}

# Runs the given apps until the merged-blocks store holds target_files files or the
# process exits (the chain exits on its own at --stop-height, taking the stack with it).
run_until_merged() {
  ../firecore -c gen.yaml start "$@" >> logs/gen.log 2>&1 &
  local pid=$!
  trap 'kill $pid 2>/dev/null || true' EXIT
  while kill -0 $pid 2>/dev/null; do
    printf '\r  merged-blocks files: %s/%s' "$(merged_count)" "$target_files"
    if [[ $(merged_count) -ge $target_files ]]; then
      break
    fi
    sleep 2
  done
  echo
  kill $pid 2>/dev/null || true
  wait $pid 2>/dev/null || true
  trap - EXIT
}

echo "Generating $THINSTORE_BLOCKS blocks (waiting for $target_files merged-blocks files)..."
: > logs/gen.log
run_until_merged reader-node merger
if [[ $(merged_count) -lt $target_files ]]; then
  echo "Chain stopped, letting the merger finish..."
  run_until_merged merger
fi
if [[ $(merged_count) -lt $target_files ]]; then
  echo "only $(merged_count) merged-blocks files, see logs/gen.log"; exit 1
fi

echo "Checking merged blocks..."
../firecore tools check merged-blocks "./$merged" 2>&1 | tail -3
echo "Done. Start the offline stack with ./start-offline.sh"
