#!/bin/sh

set -eu

dockerd --host=unix:///var/run/docker.sock &

attempt=0
while [ ! -S /var/run/docker.sock ]; do
    attempt=$((attempt + 1))
    if [ "$attempt" -gt 120 ]; then
        echo "dockerd did not start within 60 seconds" >&2
        exit 1
    fi
    sleep 0.5
done

exec "$@"
