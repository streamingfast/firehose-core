#!/usr/bin/env bash
# Full campaign against a running offline stack: scenarios, then sequential and
# concurrent fuzz seeds, then chaos seeds. Logs in logs/run-all-<timestamp>/.
#
#   ./run-all.sh          everything
#   ./run-all.sh chaos    chaos seeds only
set -uo pipefail
only=${1:-all}
cd "$(dirname "${BASH_SOURCE[0]}")"
dir=logs/run-all-$(date +%Y%m%d-%H%M%S)
mkdir -p "$dir"
rc=0
step() { # step <name> <cmd...>
  local name=$1; shift
  echo "== $name"
  if "$@" > "$dir/$name.log" 2>&1; then
    echo "   ok: $(grep -c '✓' "$dir/$name.log") passed, $(grep -c '~' "$dir/$name.log") chaos casualties"
  else
    rc=1; echo "   FAILED: $(grep -c '✗' "$dir/$name.log") failures, see $dir/$name.log"
    grep '✗' "$dir/$name.log" | head -5
  fi
}
if [[ $only == all ]]; then
  step scenarios ./test.sh
  step fuzz-seed1 python3 -u ./fuzz.py --seed 1 --iterations 6 --queries 5 --keep-going
  step fuzz-seed2 python3 -u ./fuzz.py --seed 2 --iterations 12 --queries 6 --parallel 3 --keep-going
  step fuzz-seed4 python3 -u ./fuzz.py --seed 4 --iterations 10 --queries 6 --parallel 3 --keep-going
fi
step fuzz-seed3-chaos python3 -u ./fuzz.py --seed 3 --iterations 8 --queries 6 --parallel 3 --chaos --keep-going
step fuzz-seed5-chaos python3 -u ./fuzz.py --seed 5 --iterations 8 --queries 6 --parallel 3 --chaos --keep-going
step fuzz-seed6-chaos python3 -u ./fuzz.py --seed 6 --iterations 8 --queries 6 --parallel 3 --chaos --keep-going
echo; [[ $rc -eq 0 ]] && echo "CAMPAIGN PASSED" || echo "CAMPAIGN FAILED"
echo CAMPAIGN_DONE
exit $rc
