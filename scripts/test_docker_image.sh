#!/usr/bin/env bash
#
# Smoke-tests the proxy container image (build it first, for example:
#   docker build -f cmd/proxy/Dockerfile -t athens-proxy:ci .
# ).
#
# The image must run as an unprivileged user and still work with the things the
# docs tell operators to do: use the Go toolchain, write to a named volume,
# read a mounted .netrc, and stop cleanly on SIGTERM.
#
# Usage: scripts/test_docker_image.sh <image>

set -euo pipefail

IMAGE="${1:?usage: $0 <image>}"
WORK="$(mktemp -d)"
VOLUME="athens-smoke-$$"
CONTAINER=""

cleanup() {
  if [[ -n "${CONTAINER}" ]]; then
    docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
  fi
  docker volume rm -f "${VOLUME}" >/dev/null 2>&1 || true
  rm -rf "${WORK}"
}
trap cleanup EXIT

step() { echo "== $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
expect_eq() { [[ "$1" == "$2" ]] || fail "$3: got '$1', want '$2'"; }

step "image is configured to run as the numeric user 1000:1000"
# Numeric, so Kubernetes runAsNonRoot can verify it without an explicit runAsUser.
expect_eq "$(docker inspect -f '{{.Config.User}}' "${IMAGE}")" "1000:1000" "Config.User"

step "process runs as uid 1000 and HOME is /home/athens"
expect_eq "$(docker run --rm --entrypoint id "${IMAGE}" -u)" "1000" "uid"
expect_eq "$(docker run --rm --entrypoint sh "${IMAGE}" -c 'echo "$HOME"')" "/home/athens" "HOME"

step "the binary starts"
docker run --rm "${IMAGE}" athens-proxy -version >/dev/null || fail "athens-proxy -version"

step "the go toolchain is usable and its cache directories are writable"
docker run --rm --entrypoint sh "${IMAGE}" -c \
  'go version >/dev/null && mkdir -p "$(go env GOCACHE)" "$(go env GOMODCACHE)" && touch "$(go env GOCACHE)/probe" "$(go env GOMODCACHE)/probe"' \
  || fail "go toolchain or its cache directories"

step "a Docker named volume on /var/lib/athens inherits athens ownership"
docker volume create "${VOLUME}" >/dev/null
docker run --rm -v "${VOLUME}:/var/lib/athens" --entrypoint sh "${IMAGE}" -c 'touch /var/lib/athens/probe' \
  || fail "named volume is not writable by the athens user"

step "a mounted .netrc owned by uid 1000 is readable"
printf 'machine example.com login u password p\n' > "${WORK}/netrc"
docker run --rm --user 0 -v "${WORK}:/data" --entrypoint sh "${IMAGE}" -c \
  'chown 1000:1000 /data/netrc && chmod 600 /data/netrc'
docker run --rm -v "${WORK}/netrc:/home/athens/.netrc:ro" --entrypoint sh "${IMAGE}" -c 'test -r /home/athens/.netrc' \
  || fail "mounted .netrc is not readable"

step "the server starts, serves /healthz and exits 0 on SIGTERM"
CONTAINER="$(docker run -d -p 127.0.0.1:0:3000 -e ATHENS_STORAGE_TYPE=memory "${IMAGE}")"
PORT="$(docker port "${CONTAINER}" 3000/tcp | head -n1 | sed 's/.*://')"
healthy=""
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 0.5
done
if [[ -z "${healthy}" ]]; then
  docker logs "${CONTAINER}" >&2 || true
  fail "/healthz never became healthy"
fi
docker stop -t 30 "${CONTAINER}" >/dev/null
expect_eq "$(docker inspect -f '{{.State.ExitCode}}' "${CONTAINER}")" "0" "exit code after SIGTERM"

echo "OK: ${IMAGE}"
