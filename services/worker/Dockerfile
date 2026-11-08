# syntax=docker/dockerfile:1
FROM python:3.13-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# Debian security fixes released after the base image was built (Trivy blocks fixable HIGH/CRITICAL).
RUN apt-get update \
    && apt-get upgrade -y \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
# pip is only needed while building. Removing it also removes the libraries it bundles
# (urllib3, msgpack, setuptools...), which Trivy reports even though the worker never uses them.
RUN pip install --no-compile -r requirements.txt \
    && pip uninstall -y pip

COPY worker.py healthcheck.py ./

# Run as an unprivileged user (uid 10001).
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin worker
USER 10001

# On ECS the root filesystem is read-only and an empty volume is mounted on /tmp for the
# heartbeat file. ECS gives that volume the permissions of the image's /tmp (1777) only when
# the image declares it as a VOLUME; otherwise it is root-owned 0755 and uid 10001 cannot write.
VOLUME ["/tmp"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["python", "healthcheck.py"]
CMD ["python", "worker.py"]
