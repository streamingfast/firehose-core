#!/usr/bin/env bash
# Runs firecore (reader-node + merger) with the example reader, SIGKILLs firecore and the reader
# together at random times, then runs to a target block and checks the merged blocks.
#
#   devel/reader-cursor/kill-test.sh <rewind|stateful> <legacy|cursor> [kills] [target block]
#
# firecore and the example are built from this checkout (SKIP_BUILD=1 to reuse them). WORK
# (default devel/reader-cursor/work) holds each run's data and the binaries.
set -uo pipefail

MODE=${1:?mode: rewind or stateful}
PROTOCOL=${2:?protocol: legacy or cursor}
KILLS=${3:-8}
TARGET=${4:-20000}

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-$HERE/work}
BIN=$WORK/bin
# firecore and the example's binaries, built from this checkout
if [ -z "${SKIP_BUILD:-}" ]; then
  (cd "$REPO" && go build -o "$BIN/" ./cmd/firecore ./devel/reader-cursor/fakereader ./devel/reader-cursor/checkmerged) || exit 1
fi
FIRECORE=$BIN/firecore
RATE=${RATE:-0}
PAYLOAD=${PAYLOAD:-2048}
COMMIT_INTERVAL=${COMMIT_INTERVAL:-500}
MIN_RUN=${MIN_RUN:-2}
MAX_RUN=${MAX_RUN:-5}

DIR=$WORK/$MODE-$PROTOCOL
rm -rf "$DIR"
mkdir -p "$DIR"
DATA=$DIR/firehose

ARGS="--mode=$MODE --protocol=$PROTOCOL --state-dir=$DIR/reader-state --rate=$RATE --payload-size=$PAYLOAD --commit-interval=$COMMIT_INTERVAL"
CURSOR_FLAGS=()
if [ "$PROTOCOL" = cursor ]; then
  ARGS="$ARGS --cursor-file={cursor-file} --resume-block={resume-block-num} --resume-block-id={resume-block-id} --resume-lib={resume-lib-num} --resume-cursor={resume-cursor} --resume-cursor-block={resume-cursor-block-num}"
  CURSOR_FLAGS=(--reader-node-cursor)
fi

start_firecore() {
  setsid "$FIRECORE" start reader-node merger \
    --config-file= \
    --data-dir="$DATA" \
    --log-to-file=false \
    --common-first-streamable-block=1 \
    --reader-node-path="$BIN/fakereader" \
    --reader-node-arguments="$ARGS" \
    --reader-node-blocks-chan-capacity=5000 \
    "${CURSOR_FLAGS[@]}" \
    >"$DIR/run-$1.log" 2>&1 &
  PID=$!
}

merged_last() {
  local last
  last=$(ls "$DATA/storage/merged-blocks" 2>/dev/null | sort | tail -1)
  [ -n "$last" ] && echo $((10#${last%%.*} + 99)) || echo 0
}

echo "== $MODE/$PROTOCOL: $KILLS kills, then run to $TARGET (data in $DIR)"
for i in $(seq 1 "$KILLS"); do
  start_firecore "$i"
  sleep "$(awk -v min="$MIN_RUN" -v max="$MAX_RUN" -v r="$RANDOM" 'BEGIN { printf "%.2f", min + (max - min) * r / 32767 }')"
  kill -9 -- -"$PID" 2>/dev/null
  wait "$PID" 2>/dev/null
  cursor=$(grep -o '^{"block":{"num":[0-9]*' "$DATA/reader/work/reader-cursor.json" 2>/dev/null | grep -o '[0-9]*$')
  printf '  kill %d: merged to %s, printed to %s, firecore cursor %s, reader state %s\n' "$i" "$(merged_last)" \
    "$(cat "$DIR/reader-state/last-printed" 2>/dev/null || echo -)" "${cursor:--}" \
    "$(grep -o '"height":[0-9]*' "$DIR/reader-state/state.json" 2>/dev/null | grep -o '[0-9]*' || echo -)"
done

start_firecore final
deadline=$((SECONDS + 180))
previous=-1
stalled_since=$SECONDS
while [ "$SECONDS" -lt "$deadline" ]; do
  last=$(merged_last)
  [ "$last" -ge "$TARGET" ] && break
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "  firecore exited"
    break
  fi
  if [ "$last" -ne "$previous" ]; then
    previous=$last
    stalled_since=$SECONDS
  elif [ $((SECONDS - stalled_since)) -ge 30 ]; then
    echo "  merged blocks stuck at $last for 30s"
    break
  fi
  sleep 1
done
kill -TERM -- -"$PID" 2>/dev/null
for _ in $(seq 1 20); do kill -0 "$PID" 2>/dev/null || break; sleep 1; done
kill -9 -- -"$PID" 2>/dev/null
wait "$PID" 2>/dev/null

echo "  reader errors: $(grep -h 'fakereader:.*\(state committed at .* is past\|matches\|Fatal\)' "$DIR"/run-*.log | grep -c 'is past')"
grep -h 'is past firecore' "$DIR"/run-*.log | head -1 | sed 's/^/    /'
echo "  merger 'unlinkable' warnings: $(cat "$DIR"/run-*.log | grep -c 'unlinkable')"

STATEFUL=()
[ "$MODE" = stateful ] && STATEFUL=(--stateful)
"$BIN/checkmerged" --merged-blocks="file://$DATA/storage/merged-blocks" --min-last-block="$TARGET" "${STATEFUL[@]}"
