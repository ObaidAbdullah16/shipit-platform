"""Tiny load generator (standard library only).

Creates a few short links, then hammers the redirect endpoint (without following
redirects) and the stats API for a fixed time, and prints throughput, error rate
and latency percentiles.

    python3 tools/loadgen.py http://localhost:8080 --duration 30 --concurrency 8
"""

from __future__ import annotations

import argparse
import json
import random
import statistics
import threading
import time
import urllib.error
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):  # do not follow: we measure our service only
        return None


opener = urllib.request.build_opener(NoRedirect)


def request(url: str, data: bytes | None = None) -> tuple[int, float]:
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"} if data else {})
    start = time.perf_counter()
    try:
        with opener.open(req, timeout=10) as resp:
            resp.read()
            code = resp.status
    except urllib.error.HTTPError as err:  # 3xx/4xx/5xx arrive here when not followed
        code = err.code
    except (urllib.error.URLError, TimeoutError, ConnectionError):
        code = 0
    return code, time.perf_counter() - start


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("url")
    ap.add_argument("--duration", type=int, default=60)
    ap.add_argument("--concurrency", type=int, default=8)
    ap.add_argument("--links", type=int, default=3)
    args = ap.parse_args()
    base = args.url.rstrip("/")

    codes = []
    for i in range(args.links):
        body = json.dumps({"url": f"https://example.com/loadtest/{i}"}).encode()
        req = urllib.request.Request(base + "/api/links", data=body, headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                codes.append(json.load(resp)["code"])
        except urllib.error.HTTPError as err:
            print(f"could not create a link ({err.code}): {err.read().decode()[:200]}")
    if not codes:
        print("no links available - is the service up?")
        return 1

    results: list[tuple[int, float]] = []
    lock = threading.Lock()
    stop_at = time.monotonic() + args.duration

    def worker() -> None:
        local = []
        while time.monotonic() < stop_at:
            code = random.choice(codes)
            path = f"/api/links/{code}/stats" if random.random() < 0.1 else f"/r/{code}"
            local.append(request(base + path))
        with lock:
            results.extend(local)

    print(f"Sending traffic to {base} for {args.duration}s with {args.concurrency} workers...")
    threads = [threading.Thread(target=worker) for _ in range(args.concurrency)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    lat = sorted(d for _, d in results)
    ok = sum(1 for c, _ in results if c in (200, 302))
    errors = len(results) - ok
    p = lambda q: lat[min(len(lat) - 1, int(q * len(lat)))] * 1000  # noqa: E731
    print(f"requests: {len(results)}  ({len(results) / args.duration:.1f}/s)")
    print(f"errors:   {errors}  ({100 * errors / max(1, len(results)):.2f}%)")
    print(
        f"latency:  p50 {p(0.50):.1f} ms   p95 {p(0.95):.1f} ms   p99 {p(0.99):.1f} ms   mean {statistics.mean(lat) * 1000:.1f} ms"
    )
    return 0 if errors == 0 else 2


if __name__ == "__main__":
    raise SystemExit(main())
