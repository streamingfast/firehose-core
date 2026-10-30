#!/usr/bin/env bash
# The stateful reader can't go back. With the cursor protocol, when its state is ahead of
# firecore's reader cursor (blocks it indexed were never stored), it refuses to start; once a
# snapshot at or before the cursor is restored, it indexes again up to the cursor, checks its
# state against the cursor's value and continues.
#
#   devel/reader-cursor/restore-test.sh [target block]
set -uo pipefail

TARGET=${1:-12000}

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-$HERE/work}
BIN=$WORK/bin
# firecore and the example's binaries, built from this checkout
if [ -z "${SKIP_BUILD:-}" ]; then
  (cd "$REPO" && go build -o "$BIN/" ./cmd/firecore ./devel/reader-cursor/fakereader ./devel/reader-cursor/checkmerged) || exit 1
fi
FIRECORE=$BIN/firecore
COMMIT_INTERVAL=500

DIR=$WORK/stateful-restore
rm -rf "$DIR"
mkdir -p "$DIR"
DATA=$DIR/firehose
STATE=$DIR/reader-state
CURSOR_FILE=$DATA/reader/work/reader-cursor.json

ARGS="--mode=stateful --protocol=cursor --state-dir=$STATE --payload-size=2048 --commit-interval=$COMMIT_INTERVAL"
ARGS="$ARGS --cursor-file={cursor-file} --resume-block={resume-block-num} --resume-block-id={resume-block-id} --resume-lib={resume-lib-num} --resume-cursor={resume-cursor} --resume-cursor-block={resume-cursor-block-num}"

start_firecore() {
  setsid "$FIRECORE" start reader-node merger --config-file= --data-dir="$DATA" --log-to-file=false \
    --common-first-streamable-block=1 --reader-node-path="$BIN/fakereader" --reader-node-arguments="$ARGS" \
    --reader-node-blocks-chan-capacity=5000 --reader-node-cursor >"$DIR/run-$1.log" 2>&1 &
  PID=$!
}

stop_firecore() {
  kill -TERM -- -"$PID" 2>/dev/null
  for _ in $(seq 1 20); do kill -0 "$PID" 2>/dev/null || break; sleep 1; done
  kill -9 -- -"$PID" 2>/dev/null
  wait "$PID" 2>/dev/null
}

merged_last() {
  local last
  last=$(ls "$DATA/storage/merged-blocks" 2>/dev/null | sort | tail -1)
  [ -n "$last" ] && echo $((10#${last%%.*} + 99)) || echo 0
}

cursor_block() { grep -o '^{"block":{"num":[0-9]*' "$CURSOR_FILE" | grep -o '[0-9]*$'; }
state_height() { grep -o '"height":[0-9]*' "$STATE/state.json" | grep -o '[0-9]*$'; }

wait_merged() {
  local deadline=$((SECONDS + 120))
  while [ "$(merged_last)" -lt "$1" ] && [ "$SECONDS" -lt "$deadline" ] && kill -0 "$PID" 2>/dev/null; do sleep 1; done
}

echo "== 1. index to 5000 and stop"
start_firecore 1
wait_merged 5000
stop_firecore
cursor=$(cursor_block)
echo "   firecore cursor $cursor, reader state $(state_height)"

ahead=$(( (cursor / COMMIT_INTERVAL + 2) * COMMIT_INTERVAL ))
echo "== 2. reader state ahead of the cursor ($ahead > $cursor): it must refuse to start"
"$BIN/fakereader" --mode=stateful --state-dir="$STATE" --snapshot-at="$ahead"
before=$(merged_last)
start_firecore 2
for _ in $(seq 1 20); do kill -0 "$PID" 2>/dev/null || break; sleep 1; done
stop_firecore
if grep -q "is past firecore's reader cursor" "$DIR/run-2.log"; then
  echo "   refused: $(grep -o "state committed at .*" "$DIR/run-2.log" | head -1)"
else
  echo "FAIL: the reader didn't refuse"
  exit 1
fi
echo "   merged blocks still end at $(merged_last) (were $before), cursor $(cursor_block)"

snapshot=$(( (cursor / COMMIT_INTERVAL - 2) * COMMIT_INTERVAL ))
echo "== 3. restore a snapshot at $snapshot (at or before the cursor) and run to $TARGET"
"$BIN/fakereader" --mode=stateful --state-dir="$STATE" --snapshot-at="$snapshot"
start_firecore 3
wait_merged "$TARGET"
stop_firecore
grep -h 'stateful/cursor: \(state committed\|indexed again\)' "$DIR/run-3.log" | sed 's/^fakereader: [0-9:.]* /   /'

"$BIN/checkmerged" --merged-blocks="file://$DATA/storage/merged-blocks" --min-last-block="$TARGET" --stateful
