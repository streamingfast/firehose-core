#!/usr/bin/env python3
"""Fuzzes pruned/deleted substreams caches against the offline stack.

Each iteration turns the state store into gruyère (random and adversarial deletions of
snapshots, partials, outputs and index files, per module), then runs random production
and dev-mode queries over ranges chosen around the holes and compares every block's
output with out/baseline.jsonl. Any mismatch, failed request, or tier1 log line about a
missing snapshot stops the run (unless --keep-going) and writes a replayable report.

    ./fuzz.py --seed 1 --iterations 20 --queries 6
    ./fuzz.py --replay out/fuzz-failure-1-7.json      # re-applies the same deletions + queries

Requires out/baseline.jsonl (run ./test.sh once) and start-offline.sh running.
"""
import argparse, json, os, random, re, subprocess, sys, threading, time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

ROOT = Path(__file__).resolve().parent
STATE = ROOT / "firehose-data/localdata"
SPKG = ROOT / "substreams/thinstore-test.spkg"
LOG = ROOT / "logs/offline.log"
OUT = ROOT / "out"
SEGMENT = 100

BAD_LOG = re.compile(r"does not exist \(it may have been pruned\)|panic|invalid transition|assertion")
# request stats lines repeat the request's error, they are not a separate event
IGNORED_LOG = re.compile(r"substreams request stats")
# in --chaos mode a request may legitimately lose a file it was about to read
CHAOS_CASUALTY = re.compile(r"opening file for streaming: not found|does not exist \(it may have been pruned\)")


def module_hashes():
    info = subprocess.run(["substreams", "info", str(SPKG)], capture_output=True, text=True).stdout
    out, name = {}, None
    for line in info.splitlines():
        if line.startswith("Name:"):
            name = line.split()[1]
        elif line.startswith("Hash:") and name:
            out[name] = line.split()[1]
    return out


def load_baseline():
    rows = {}
    for line in open(OUT / "baseline.jsonl"):
        if line.startswith("{"):
            d = json.loads(line)
            if "@block" in d:
                rows[d["@block"]] = d["@data"]
    return rows


def list_cache(hashes):
    """{module: {kind: {block: path}}} where block is the file's start (outputs/index) or end (states)."""
    cache = {}
    for name, h in hashes.items():
        for kind in ("states", "outputs", "index"):
            d = STATE / h / kind
            if not d.is_dir():
                continue
            files = {}
            for f in d.iterdir():
                m = re.match(r"(\d{10})-(\d{10})\.(kv|output|index|partial)", f.name)
                if not m:
                    continue
                key = int(m.group(1))
                files.setdefault(m.group(3), {})[key] = f
            cache[name] = {**cache.get(name, {}), **{f"{kind}:{ext}": v for ext, v in files.items()}}
    return cache


# --- deletion strategies: each returns the list of files it deleted -------------------------

def strat_random(rng, files, p):
    return [f for f in files.values() if rng.random() < p]

def strat_keep_every(rng, files, every):
    return [f for b, f in files.items() if b % every != 0]

def strat_contiguous(rng, files, span):
    keys = sorted(files)
    if not keys:
        return []
    start = rng.choice(keys)
    return [files[b] for b in keys if start <= b < start + span]

def strat_all_below(rng, files, block):
    return [f for b, f in files.items() if b <= block]

def strat_all_but_few(rng, files, keep):
    keys = sorted(files)
    kept = set(rng.sample(keys, min(keep, len(keys))))
    return [files[b] for b in keys if b not in kept]

def strat_everything(rng, files):
    return list(files.values())

def strat_odd_boundaries(rng, files, every):
    # keep boundaries that are NOT multiples of `every`: stores end up with no common kept boundary
    return [f for b, f in files.items() if b % every == 0]


def gruyere(rng, cache, last):
    """Applies a random deletion plan; returns a serializable description of what was deleted."""
    plan = []
    store_names = [m for m in cache if "states:kv" in cache[m]]
    # one module of each kind gets an adversarial treatment, the rest get random holes
    for name, kinds in cache.items():
        for kind, files in kinds.items():
            r = rng.random()
            if not files:
                continue
            if kind == "states:kv":
                choice = rng.choices(
                    ["random", "keep_every", "contiguous", "all_below", "all_but_few", "everything", "odd", "none"],
                    weights=[4, 4, 3, 2, 2, 2, 2, 2])[0]
            elif kind == "states:partial":
                choice = rng.choice(["random", "everything", "none"])
            else:
                choice = rng.choices(["random", "contiguous", "all_below", "everything", "none"],
                                     weights=[4, 3, 2, 1, 3])[0]
            if choice == "random":
                deleted = strat_random(rng, files, rng.choice([0.1, 0.5, 0.9, 0.98]))
            elif choice == "keep_every":
                deleted = strat_keep_every(rng, files, rng.choice([300, 1000, 2000, 5000, 7700]))
            elif choice == "contiguous":
                deleted = strat_contiguous(rng, files, rng.choice([300, 1000, 5000, 15000]))
            elif choice == "all_below":
                deleted = strat_all_below(rng, files, rng.randrange(0, last, SEGMENT))
            elif choice == "all_but_few":
                deleted = strat_all_but_few(rng, files, rng.choice([1, 2, 3]))
            elif choice == "everything":
                deleted = strat_everything(rng, files)
            elif choice == "odd":
                deleted = strat_odd_boundaries(rng, files, rng.choice([1000, 2000]))
            else:
                deleted = []
            for f in deleted:
                f.unlink(missing_ok=True)
            plan.append({"module": name, "kind": kind, "strategy": choice,
                         "deleted": sorted(f.name for f in deleted), "left": len(files) - len(deleted)})
    return plan


def apply_plan(plan, hashes):
    for p in plan:
        kind = p["kind"].split(":")[0]
        for name in p["deleted"]:
            (STATE / hashes[p["module"]] / kind / name).unlink(missing_ok=True)


def chaos_loop(rng, hashes, stop, deleted):
    """Deletes a few random cache files every few hundred ms until stopped."""
    while not stop.wait(rng.uniform(0.2, 1.0)):
        cache = list_cache(hashes)
        victims = [(m, k, f) for m, kinds in cache.items() for k, files in kinds.items() for f in files.values()]
        if not victims:
            continue
        for m, k, f in rng.sample(victims, min(len(victims), rng.choice([1, 5, 20]))):
            f.unlink(missing_ok=True)
            deleted.append(f"{m}/{k.split(':')[0]}/{f.name}")


# --- queries ---------------------------------------------------------------------------------

def random_range(rng, last, plan):
    """Ranges biased towards hole edges: a deleted file boundary +/- a few blocks."""
    edges = [0, last]
    for p in plan:
        for name in p["deleted"][:50]:
            edges.append(int(name[:10]))
    kind = rng.choices(["edge", "uniform", "long", "tiny", "tail"], weights=[5, 3, 1, 2, 1])[0]
    if kind == "edge":
        start = max(0, rng.choice(edges) + rng.choice([-150, -101, -100, -1, 0, 1, 50, 99, 100, 101]))
        length = rng.choice([1, 2, 50, 100, 101, 250, 1000])
    elif kind == "uniform":
        start = rng.randrange(0, last)
        length = rng.choice([10, 100, 300, 1000, 2500])
    elif kind == "long":
        start = rng.randrange(0, last // 2)
        length = rng.randrange(3000, 15000)
    elif kind == "tiny":
        start = rng.randrange(0, last)
        length = 1
    else:
        start = last - rng.choice([1, 10, 100, 350, 1000])
        length = last
    stop = min(last, start + length)
    if stop <= start:
        start, stop = max(0, last - 100), last
    mode = rng.choices(["prod", "dev"], weights=[3, 1])[0]
    return {"start": start, "stop": stop, "mode": mode}


def run_query(q, name, timeout):
    cmd = ["substreams", "run", str(SPKG), "map_out", "-e", "localhost:10016", "--plaintext",
           "--limit-processed-blocks", "0", "-o", "jsonl", "-s", str(q["start"]), "-t", str(q["stop"])]
    if q["mode"] == "prod":
        cmd.append("--production-mode")
    t0 = time.time()
    with open(OUT / f"{name}.jsonl", "w") as f, open(OUT / f"{name}.err", "w") as e:
        proc = subprocess.Popen(cmd, stdout=f, stderr=e)
        try:
            rc = proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            # the request is still alive on tier1: capture its state before killing the client
            capture_hang(name, q)
            proc.kill()
            proc.wait()
            e.write(f"HUNG: no completion after {timeout}s (killed)\n")
            rc = -1
    return rc, time.time() - t0


def capture_hang(name, q):
    """Saves tier1's goroutine dump and the hung request's last progress line to out/<name>.hang.txt."""
    with open(OUT / f"{name}.hang.txt", "w") as h:
        h.write(f"query: {q}\n\n== last progress lines for start_block {q['start']}\n")
        try:
            log = LOG.read_text(errors="replace")
            m = re.findall(r'"trace_id": "([a-f0-9]+)".*"start_block": %d, "stop_block": %d' % (q["start"], q["stop"]), log)
            if m:
                trace = m[-1]
                lines = [l for l in log.splitlines() if trace in l and ("request progress" in l or "resume point" in l)]
                h.write("\n".join(lines[-3:]) + "\n")
        except Exception as exc:  # diagnostics only
            h.write(f"(log scan failed: {exc})\n")
        h.write("\n== goroutines\n")
        try:
            h.write(subprocess.run(["curl", "-s", "-m", "10", "http://localhost:6060/debug/pprof/goroutine?debug=2"],
                                   capture_output=True, text=True).stdout)
        except Exception as exc:
            h.write(f"(pprof failed: {exc})\n")


def compare(name, q, baseline):
    got = {}
    for line in open(OUT / f"{name}.jsonl"):
        if line.startswith("{"):
            d = json.loads(line)
            if "@block" in d:
                got[d["@block"]] = d["@data"]
    want = {b: v for b, v in baseline.items() if q["start"] <= b < q["stop"]}
    if want == got:
        return None
    missing = sorted(set(want) - set(got))[:5]
    extra = sorted(set(got) - set(want))[:5]
    diff = [b for b in sorted(set(want) & set(got)) if want[b] != got[b]][:5]
    return f"baseline={len(want)} got={len(got)} missing={missing} extra={extra} differing={diff}"


def log_tail(mark):
    with open(LOG) as f:
        f.seek(mark)
        return f.read()


def resume_points(text):
    return re.findall(r'"target_segment": (-?\d+), "resume_segment": (-?\d+)', text)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=int(time.time()) % 100000)
    ap.add_argument("--iterations", type=int, default=10)
    ap.add_argument("--queries", type=int, default=5, help="queries per iteration")
    ap.add_argument("--parallel", type=int, default=1,
                    help="concurrent queries per iteration (tier1 allows 3 sessions per organization)")
    ap.add_argument("--last", type=int, default=30000)
    ap.add_argument("--keep-going", action="store_true")
    ap.add_argument("--timeout", type=int, default=600,
                    help="seconds a single query may take before it is reported as hung")
    ap.add_argument("--chaos", action="store_true",
                    help="keep deleting random cache files while the queries run")
    ap.add_argument("--replay", help="failure report to replay")
    args = ap.parse_args()

    hashes = module_hashes()
    baseline = load_baseline()
    if not baseline:
        sys.exit("no out/baseline.jsonl, run ./test.sh first")
    failures = 0

    if args.replay:
        report = json.load(open(args.replay))
        iterations = [(report["plan"], report["queries"])]
        print(f"replaying {args.replay}")
    else:
        rng = random.Random(args.seed)
        iterations = None
        print(f"seed={args.seed} iterations={args.iterations} queries={args.queries} parallel={args.parallel}")

    for it in range(args.iterations if not args.replay else 1):
        if args.replay:
            plan, queries = iterations[0]
            apply_plan(plan, hashes)
        else:
            cache = list_cache(hashes)
            plan = gruyere(rng, cache, args.last)
            queries = [random_range(rng, args.last, plan) for _ in range(args.queries)]
        left = {p["module"] + "/" + p["kind"].split(":")[1]: p["left"] for p in plan if p["strategy"] != "none"}
        print(f"-- iteration {it}: deleted "
              + ", ".join(f"{p['module']}/{p['kind'].split(':')[1]}:{p['strategy']}({len(p['deleted'])} gone, {p['left']} left)"
                          for p in plan if p["deleted"]))

        mark = LOG.stat().st_size
        names = [f"fuzz-{args.seed}-{it}-{i}" for i in range(len(queries))]
        stop_chaos = threading.Event()
        chaos_deleted = []
        if args.chaos and not args.replay:
            chaos = threading.Thread(target=chaos_loop, args=(rng, hashes, stop_chaos, chaos_deleted), daemon=True)
            chaos.start()
        with ThreadPoolExecutor(max_workers=args.parallel) as pool:
            results = list(pool.map(lambda nq: run_query(nq[1], nq[0], args.timeout), zip(names, queries)))
        stop_chaos.set()
        if chaos_deleted:
            print(f"    chaos deleted {len(chaos_deleted)} files while queries ran")
            plan.append({"module": "*", "kind": "chaos:*", "strategy": "chaos", "deleted": chaos_deleted, "left": -1})
        tail = log_tail(mark)
        rp = resume_points(tail)
        bad_log = [l for l in tail.splitlines() if BAD_LOG.search(l) and not IGNORED_LOG.search(l)]
        if args.chaos:
            bad_log = [l for l in bad_log if not CHAOS_CASUALTY.search(l)]

        for name, q, (rc, dt) in zip(names, queries, results):
            label = f"{q['mode']:4} [{q['start']},{q['stop']})"
            problem, casualty = None, False
            if rc != 0:
                err = open(OUT / f"{name}.err").read().strip().splitlines()
                problem = "request failed: " + (err[-1] if err else "?")
                casualty = args.chaos and rc != -1 and bool(CHAOS_CASUALTY.search(problem))
            else:
                problem = compare(name, q, baseline)
            if casualty:
                print(f"  ~ {label} {dt:.0f}s: lost a file to chaos, failed cleanly")
            elif problem:
                failures += 1
                print(f"  ✗ {label} {dt:.0f}s: {problem}")
            else:
                print(f"  ✓ {label} {dt:.0f}s")
        if bad_log:
            failures += 1
            print("  ✗ tier1 log: " + bad_log[0][:300])
        if rp:
            print("    resume points: " + " ".join(f"{t}->{r}" for t, r in rp[:8]))
        if failures and not args.keep_going or (failures and args.replay):
            report = OUT / f"fuzz-failure-{args.seed}-{it}.json"
            json.dump({"seed": args.seed, "iteration": it, "plan": plan, "queries": queries,
                       "bad_log": bad_log[:20]}, open(report, "w"), indent=1)
            print(f"\nFAILED, report written to {report} (replay with --replay)")
            sys.exit(1)

    print("\nALL PASSED" if not failures else f"\n{failures} FAILURE(S)")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
