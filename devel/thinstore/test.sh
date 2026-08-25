#!/usr/bin/env bash
# Exercises pruned store snapshots, deleted mapper outputs and deleted index files against
# the offline stack (start-offline.sh must be running on :10016). Re-runnable: the state
# store is wiped at start.
#
#   ./test.sh [-k]      -k keeps the state store from a previous run (skips the baseline)
#
# Flow:
#   1. wipe state, run map_out over the whole range in production mode -> baseline output
#      and every snapshot (one per 100 blocks), output and index file on disk
#   2. prune snapshots with `firecore tools substreams prune-states`, then punch holes:
#      different extra snapshot holes per store and stage, deleted mapper outputs
#      (intermediate and final) and deleted index files over various ranges
#   3. run production-mode and dev-mode requests over ranges crossing those holes and
#      compare every block's output to the baseline; a final full-range run must
#      reproduce the baseline entirely
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

ENDPOINT=${ENDPOINT:-localhost:10016}
SPKG=substreams/thinstore-test.spkg
STATE=firehose-data/localdata
LOG=logs/offline.log
LAST=${LAST:-30000}     # exclusive end of the baseline run, must be <= merged blocks
KEEP_EVERY=${KEEP_EVERY:-1000}
OUT=out
firecore=../firecore
substreams=${SUBSTREAMS_BIN:-substreams}

keep=
while getopts "hk" opt; do
  case $opt in
    k) keep=true;;
    h) sed -n '2,17p' "$0"; exit 0;;
    *) exit 1;;
  esac
done

mkdir -p "$OUT" logs
failures=0
pass() { echo "  ✓ $*"; }
fail() { echo "  ✗ $*"; failures=$((failures + 1)); }

if ! nc -z localhost "${ENDPOINT##*:}" 2>/dev/null; then
  echo "tier1 not reachable on $ENDPOINT, run ./start-offline.sh first"; exit 1
fi

# module name -> module hash, from the package itself
declare -A HASH
while read -r name hash; do HASH[$name]=$hash; done < <(
  $substreams info "$SPKG" 2>/dev/null | awk '/^Name:/{n=$2} /^Hash:/{print n, $2}')
[[ ${#HASH[@]} -gt 0 ]] || { echo "cannot read module hashes from $SPKG"; exit 1; }

# run <name> <start> <stop> [extra substreams flags...]: streams map_out into out/<name>.jsonl
run() {
  local name=$1 start=$2 stop=$3; shift 3
  local t0=$SECONDS
  if ! $substreams run "$SPKG" map_out -e "$ENDPOINT" --plaintext --limit-processed-blocks 0 -o jsonl -s "$start" -t "$stop" "$@" \
      > "$OUT/$name.jsonl" 2> "$OUT/$name.err"; then
    fail "$name: substreams run failed ($(tail -1 "$OUT/$name.err"))"
    return 1
  fi
  echo "    $name [$start,$stop) took $((SECONDS - t0))s"
}

# expect <name> <start> <stop>: compares out/<name>.jsonl with the baseline slice
expect() {
  local name=$1 start=$2 stop=$3
  python3 - "$OUT/baseline.jsonl" "$OUT/$name.jsonl" "$start" "$stop" <<'PY'
import json, sys
base, got, start, stop = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
def rows(path, lo, hi):
    out = {}
    for line in open(path):
        line = line.strip()
        if not line.startswith("{"):
            continue
        d = json.loads(line)
        if "@block" not in d:
            continue
        if lo <= d["@block"] < hi:
            out[d["@block"]] = d["@data"]
    return out
want, have = rows(base, start, stop), rows(got, start, stop)
if want == have and len(have) > 0:
    sys.exit(0)
missing = sorted(set(want) - set(have))[:5]
extra = sorted(set(have) - set(want))[:5]
diff = [b for b in sorted(set(want) & set(have)) if want[b] != have[b]][:5]
print(f"    baseline={len(want)} got={len(have)} missing={missing} extra={extra} differing={diff}")
for b in diff[:2]:
    print(f"    block {b}: want {want[b]} got {have[b]}")
sys.exit(1)
PY
}

# check <name> <start> <stop> [flags]: run + compare + look for load errors in tier1 log
check() {
  local name=$1 start=$2 stop=$3; shift 3
  local logmark=$(wc -l < "$LOG" | tr -d ' ')
  run "$name" "$start" "$stop" "$@" || return
  local resume=$(tail -n +"$logmark" "$LOG" | grep -o '"target_segment": [0-9-]*, "resume_segment": [0-9-]*' | tail -1 | sed 's/"//g')
  if expect "$name" "$start" "$stop"; then
    pass "$name ${resume:+($resume)}"
  else
    fail "$name: output differs from baseline"
  fi
  if tail -n +"$logmark" "$LOG" | grep -q 'does not exist (it may have been pruned)'; then
    fail "$name: tier1 tried to load a pruned snapshot (see $LOG)"
  fi
}

count_files() { find "$STATE" -path "*/$1/*" -type f | wc -l | tr -d ' '; }

# del_snapshots <module> <end block>...: deletes the fullKV ending at those blocks
del_snapshots() {
  local mod=$1; shift
  for b in "$@"; do rm -f "$STATE/${HASH[$mod]}/states/$(printf %010d "$b")-"*.kv*; done
  echo "    $mod: removed snapshots $*"
}

# del_files <module> <outputs|index> <from> <to>: deletes the 100-blocks files in [from, to)
del_files() {
  local mod=$1 kind=$2 from=$3 to=$4
  for ((b = from; b < to; b += 100)); do rm -f "$STATE/${HASH[$mod]}/$kind/$(printf %010d "$b")-"*; done
  echo "    $mod: removed $kind [$from,$to)"
}

echo "== 1. baseline (0 -> $LAST, production mode)"
if [[ $keep != true ]]; then
  rm -rf "$STATE"
  run baseline 0 "$LAST" --production-mode || { echo "baseline failed"; exit 1; }
fi
echo "    on disk: $(count_files states) snapshots, $(count_files outputs) outputs, $(count_files index) index files"
[[ $(count_files states) -gt 0 ]] || { echo "no snapshots written, aborting"; exit 1; }

echo "== 2. prune (keep every $KEEP_EVERY blocks) and punch holes"
$firecore tools substreams prune-states "$STATE" --keep-every "$KEEP_EVERY" --keep-recent 1 2>&1 | grep 'snapshot(s)' | sed 's/^/    /'
# stage 1
del_snapshots store_count 10000 20000
del_files map_a outputs 0 3000
del_files map_a outputs 12000 13000
del_files index_parity index 1000 2000
del_files index_parity index 14000 15000
del_files index_parity index 25000 26000
# stage 2
del_snapshots store_sum 3000 4000 20000
del_snapshots store_a_last 15000
del_files map_b outputs 2000 2500
del_files map_b outputs 15000 16000
del_files map_b outputs 28000 29000
# stage 3
del_snapshots store_c 5000 6000 7000 8000 9000
del_files map_c outputs 6000 6500
del_files map_c outputs 21000 "$LAST"
# stage 4 (output module)
del_files map_out outputs 0 2500
del_files map_out outputs 3000 3600
del_files map_out outputs 5000 8000
del_files map_out outputs 10000 12000
del_files map_out outputs 15000 15200
del_files map_out outputs 19000 21000
del_files map_out outputs 24000 24300
del_files map_out outputs 26500 27500
del_files map_out outputs 29000 "$LAST"
echo "    on disk: $(count_files states) snapshots, $(count_files outputs) outputs, $(count_files index) index files"

echo "== 3. production mode"
check outputs_present_near_head        28000 29000 --production-mode
check outputs_partially_present         2300  2700 --production-mode
check outputs_missing_then_present     24200 24500 --production-mode
check outputs_present_then_missing     26300 26800 --production-mode
check genesis_all_missing                  0   500 --production-mode
check index_hole_and_outputs_missing    1100  1300 --production-mode
check index_hole_outputs_present       14000 14500 --production-mode
check stage2_holes                      3200  3500 --production-mode
check stage3_big_hole                   6000  6500 --production-mode
check stage3_big_hole_to_kept           7300  8200 --production-mode
check stage1_and_stage2_hole_at_20000  19500 20500 --production-mode
check stage2_a_last_hole               15000 15300 --production-mode
check upper_stages_missing             26600 26900 --production-mode
check far_range                        10500 11000 --production-mode
check tail_all_missing                 29000 "$LAST" --production-mode
echo "== 4. dev mode"
check dev_stage3_big_hole               6100  6150
check dev_stage2_a_last_hole           15050 15100
check dev_tail                         29950 "$LAST"
echo "== 5. whole range again (every hole at once)"
check full_range 0 "$LAST" --production-mode
echo "    on disk: $(count_files states) snapshots, $(count_files outputs) outputs, $(count_files index) index files"
logmark=$(wc -l < "$LOG" | tr -d ' ')
check rerun_stage3_big_hole 6000 6500 --production-mode
if tail -n +"$logmark" "$LOG" | grep -q '"resume_segment": -1'; then
  fail "rerun_stage3_big_hole: tier1 resumed from scratch on a rebuilt range"
fi

echo
if [[ $failures -eq 0 ]]; then
  echo "ALL PASSED"
else
  echo "$failures FAILURE(S)"; exit 1
fi
