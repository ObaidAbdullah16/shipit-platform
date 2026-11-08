"""Container health check: the worker loop must have written a heartbeat recently."""

import os
import sys
import time

path = os.environ.get("HEARTBEAT_FILE", "/tmp/worker-heartbeat")
try:
    with open(path, encoding="utf-8") as fh:
        age = time.time() - int(fh.read().strip() or 0)
except (OSError, ValueError):
    sys.exit(1)
sys.exit(0 if age < 60 else 1)
