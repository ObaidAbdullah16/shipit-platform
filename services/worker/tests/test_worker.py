import json
import os
import socket

import pytest

import worker


def ev(code, ts=1_760_000_000):
    return json.dumps({"code": code, "ts": ts})


def test_aggregate_counts_per_day_and_code():
    day2 = 1_760_000_000 + 86_400
    per_day, per_code, bad = worker.aggregate([ev("a"), ev("a"), ev("b"), ev("a", day2), "garbage", '{"code": 1}'])
    assert per_code == {"a": 3, "b": 1}
    assert per_day[("a", "2025-10-09")] == 2
    assert per_day[("a", "2025-10-10")] == 1
    assert bad == 2


def test_database_url_from_parts(monkeypatch):
    monkeypatch.delenv("DATABASE_URL", raising=False)
    monkeypatch.setenv("DB_HOST", "db.example")
    monkeypatch.setenv("DB_PASSWORD", "p@ss/w:rd")
    url = worker.database_url()
    assert url == "postgresql://shipit:p%40ss%2Fw%3Ard@db.example:5432/shipit?sslmode=require"


def test_database_url_required(monkeypatch):
    monkeypatch.delenv("DATABASE_URL", raising=False)
    monkeypatch.delenv("DB_HOST", raising=False)
    with pytest.raises(SystemExit):
        worker.database_url()


# ---------------------------------------------------------------- integration
# Runs when REDIS_ADDR and TEST_DATABASE_URL point to disposable services
# (CI starts redis and postgres service containers).


def _reachable(addr):
    host, _, port = addr.partition(":")
    try:
        socket.create_connection((host, int(port or 6379)), timeout=1).close()
        return True
    except OSError:
        return False


needs_services = pytest.mark.skipif(
    not (
        os.environ.get("REDIS_ADDR")
        and os.environ.get("TEST_DATABASE_URL")
        and _reachable(os.environ.get("REDIS_ADDR", ""))
    ),
    reason="REDIS_ADDR / TEST_DATABASE_URL not set",
)


@pytest.fixture
def services():
    import psycopg
    import redis

    r = redis.Redis.from_url("redis://" + os.environ["REDIS_ADDR"], decode_responses=True)
    conn = psycopg.connect(os.environ["TEST_DATABASE_URL"], autocommit=True)
    conn.execute(
        """CREATE TABLE IF NOT EXISTS links (code text PRIMARY KEY, url text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
           CREATE TABLE IF NOT EXISTS daily_clicks (code text NOT NULL REFERENCES links(code) ON DELETE CASCADE,
               day date NOT NULL, clicks bigint NOT NULL DEFAULT 0, PRIMARY KEY (code, day));"""
    )
    conn.execute("DELETE FROM links WHERE code IN ('wk1', 'wk2')")
    conn.execute("INSERT INTO links (code, url) VALUES ('wk1', 'https://example.com')")
    r.delete(worker.QUEUE, "pending:wk1", "pending:ghost")
    yield r, conn
    conn.execute("DELETE FROM links WHERE code IN ('wk1', 'wk2')")
    r.delete(worker.QUEUE, "pending:wk1", "pending:ghost")
    conn.close()


@needs_services
def test_process_once_end_to_end(services):
    r, conn = services
    for _ in range(3):
        r.lpush(worker.QUEUE, ev("wk1"))
        r.incr("pending:wk1")
    r.lpush(worker.QUEUE, ev("ghost"))  # link that does not exist: skipped, not an error
    r.incr("pending:ghost")

    assert worker.process_once(r, conn, block_seconds=1) == 4
    assert conn.execute("SELECT clicks FROM daily_clicks WHERE code = 'wk1'").fetchone()[0] == 3
    assert int(r.get("pending:wk1")) == 0
    assert r.llen(worker.QUEUE) == 0
    assert worker.process_once(r, conn, block_seconds=1) == 0

    r.lpush(worker.QUEUE, ev("wk1"))
    worker.process_once(r, conn, block_seconds=1)
    assert conn.execute("SELECT clicks FROM daily_clicks WHERE code = 'wk1'").fetchone()[0] == 4


@needs_services
def test_events_are_requeued_when_database_fails(services):
    r, conn = services

    class Broken:
        def transaction(self):
            raise worker.psycopg.OperationalError("database went away")

    r.lpush(worker.QUEUE, ev("wk1"), ev("wk1"))
    with pytest.raises(worker.psycopg.OperationalError):
        worker.process_once(r, Broken(), block_seconds=1)
    assert r.llen(worker.QUEUE) == 2  # nothing lost
    assert worker.process_once(r, conn, block_seconds=1) == 2
