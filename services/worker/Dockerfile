# syntax=docker/dockerfile:1
FROM python:3.13-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-compile -r requirements.txt

COPY worker.py healthcheck.py ./

# Run as an unprivileged user (uid 10001).
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin worker
USER 10001

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["python", "healthcheck.py"]
CMD ["python", "worker.py"]
