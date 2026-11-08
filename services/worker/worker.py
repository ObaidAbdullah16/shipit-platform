"""ShipIt click worker.

The API pushes one JSON event per redirect onto the Redis list ``clicks`` and
increments ``pending:<code>``. This worker drains the list in batches, adds the
counts to ``daily_clicks`` in Postgres (one transaction per batch) and then
decrements the pending counters, so the stats endpoint always shows
processed + pending.

If Postgres is unavailable the batch is pushed back onto the queue, so no
clicks are lost (at-least-once processing).
"""

from __future__ import annotations

import json
import logging
import os
import signal
import sys
import time
from collections import Counter
from datetime import UTC, datetime
from urllib.parse import quote

import psycopg
import redis

QUEUE = "clicks"
BATCH = int(os.environ.get("BATCH_SIZE", "500"))
HEARTBEAT_FILE = os.environ.get("HEARTBEAT_FILE", "/tmp/worker-heartbeat")

log = logging.getLogger("shipit-worker")


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        data = {
            "time": datetime.now(UTC).isoformat(timespec="milliseconds"),
            "level": record.levelname,
            "service": "shipit-worker",
            "msg": record.getMessage(),
        }
        data.update(getattr(record, "fields", {}))
        return json.dumps(data)


def database_url() -> str:
    """DATABASE_URL wins; otherwise build it from DB_* (ECS injects DB_PASSWORD from Secrets Manager)."""
    if url := os.environ.get("DATABASE_URL"):
        return url
    host = os.environ.get("DB_HOST")
    if not host:
        raise SystemExit("set DATABASE_URL or DB_HOST/DB_USER/DB_PASSWORD/DB_NAME")
    user = quote(os.environ.get("DB_USER", "shipit"), safe="")
    password = quote(os.environ.get("DB_PASSWORD", ""), safe="")
    port = os.environ.get("DB_PORT", "5432")
    name = os.environ.get("DB_NAME", "shipit")
    sslmode = os.environ.get("DB_SSLMODE", "require")
    return f"postgresql://{user}:{password}@{host}:{port}/{name}?sslmode={sslmode}"


def aggregate(events: list[str]) -> tuple[Counter, Counter, int]:
    """Count clicks per (code, day) and per code. Malformed events are skipped."""
    per_day: Counter = Counter()
    per_code: Counter = Counter()
    bad = 0
    for raw in events:
        try:
            ev = json.loads(raw)
            code = str(ev["code"])
            day = datetime.fromtimestamp(int(ev["ts"]), UTC).date().isoformat()
        except (ValueError, KeyError, TypeError):
            bad += 1
            continue
        per_day[(code, day)] += 1
        per_code[code] += 1
    return per_day, per_code, bad


UPSERT = """
INSERT INTO daily_clicks (code, day, clicks)
SELECT %(code)s, %(day)s, %(n)s
WHERE EXISTS (SELECT 1 FROM links WHERE code = %(code)s)
ON CONFLICT (code, day) DO UPDATE SET clicks = daily_clicks.clicks + EXCLUDED.clicks
"""


def write_batch(conn: psycopg.Connection, per_day: Counter) -> None:
    rows = [{"code": c, "day": d, "n": n} for (c, d), n in per_day.items()]
    with conn.transaction(), conn.cursor() as cur:
        cur.executemany(UPSERT, rows)


def pop_batch(r: redis.Redis, block_seconds: int = 5) -> list[str]:
    """Block for the first event, then take up to BATCH-1 more without blocking."""
    first = r.brpop([QUEUE], timeout=block_seconds)
    if first is None:
        return []
    events = [first[1]]
    more = r.rpop(QUEUE, BATCH - 1)
    if more:
        events.extend(more)
    return events


def process_once(r: redis.Redis, conn: psycopg.Connection, block_seconds: int = 5) -> int:
    """Process one batch. Returns the number of events consumed."""
    events = pop_batch(r, block_seconds)
    if not events:
        return 0
    per_day, per_code, bad = aggregate(events)
    try:
        if per_day:
            write_batch(conn, per_day)
    except Exception:
        # Put the events back at the consuming end so they are retried first.
        r.rpush(QUEUE, *reversed(events))
        raise
    pipe = r.pipeline(transaction=False)
    for code, n in per_code.items():
        pipe.decrby(f"pending:{code}", n)
    pipe.execute()
    log.info("batch processed", extra={"fields": {"events": len(events), "codes": len(per_code), "malformed": bad}})
    return len(events)


_heartbeat_warned = False


def heartbeat() -> None:
    """Touch the file the container health check reads. If it cannot be written the task is
    marked unhealthy and replaced, so say why once instead of failing silently."""
    global _heartbeat_warned
    try:
        with open(HEARTBEAT_FILE, "w", encoding="utf-8") as fh:
            fh.write(str(int(time.time())))
    except OSError as exc:
        if not _heartbeat_warned:
            log.error(
                "cannot write the heartbeat file: the health check will fail",
                extra={"fields": {"path": HEARTBEAT_FILE, "error": str(exc)}},
            )
            _heartbeat_warned = True


def wait_for_schema(conn: psycopg.Connection, timeout: float = 120) -> None:
    """The API creates the schema on start; wait until the table exists."""
    deadline = time.monotonic() + timeout
    while True:
        row = conn.execute("SELECT to_regclass('public.daily_clicks')").fetchone()
        if row and row[0]:
            return
        if time.monotonic() > deadline:
            raise SystemExit("daily_clicks table not found: is the API running?")
        log.info("waiting for the API to create the schema")
        heartbeat()  # still alive while waiting
        time.sleep(3)


def main() -> int:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    logging.basicConfig(level=logging.INFO, handlers=[handler])

    stopping = False

    def stop(signum, _frame):
        nonlocal stopping
        log.info("stopping", extra={"fields": {"signal": signum}})
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)

    r = redis.Redis.from_url(
        os.environ.get("REDIS_URL", "redis://" + os.environ.get("REDIS_ADDR", "localhost:6379")),
        decode_responses=True,
        socket_timeout=10,
        health_check_interval=30,
    )
    backoff = 1.0
    conn = None
    log.info("worker started", extra={"fields": {"batch": BATCH}})
    while not stopping:
        heartbeat()
        try:
            if conn is None or conn.closed:
                conn = psycopg.connect(database_url(), autocommit=True, connect_timeout=5)
                wait_for_schema(conn)
            process_once(r, conn, block_seconds=2)
            backoff = 1.0
        except (redis.RedisError, psycopg.Error, OSError) as exc:
            log.warning("retrying after error", extra={"fields": {"error": str(exc), "sleep_s": backoff}})
            if conn is not None and conn.broken:
                conn = None
            time.sleep(backoff)
            backoff = min(backoff * 2, 30)
    if conn is not None:
        conn.close()
    log.info("worker stopped")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
