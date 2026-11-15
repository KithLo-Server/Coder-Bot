#!/bin/sh

set -eu

dns_args=""
while read -r key value _rest; do
    [ "$key" = "nameserver" ] || continue
    [ "$value" = "127.0.0.11" ] && continue
    dns_args="$dns_args --dns $value"
done < /etc/resolv.conf
if [ -z "$dns_args" ]; then
    dns_args="--dns 8.8.8.8 --dns 1.1.1.1"
fi

# shellcheck disable=SC2086
dockerd --host=unix:///var/run/docker.sock $dns_args &

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
