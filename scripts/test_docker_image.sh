#!/usr/bin/env bash
#
# Smoke-tests the proxy container image (build it first, for example:
#   docker build -f cmd/proxy/Dockerfile -t athens-proxy:ci .
# ).
#
# The image must run as an unprivileged user and still work with the things the
# docs tell operators to do: use the Go toolchain, write to a named volume,
# read a mounted .netrc, let Athens write credentials under $HOME, fetch a real
# module through the proxy, run under an arbitrary uid in group 0 (OpenShift),
# and stop cleanly on SIGTERM. It also asserts the documented breaking case: a
# root-owned bind mount on /var/lib/athens is not writable.
#
# Usage: scripts/test_docker_image.sh <image>

set -euo pipefail

IMAGE="${1:?usage: $0 <image>}"
WORK="$(mktemp -d)"
VOLUME="athens-smoke-$$"
CONTAINER=""
PORT=""

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

# start_server <docker run args...>
# Starts the image detached, publishes 3000 on a random loopback port, waits for
# /healthz, and sets CONTAINER and PORT.
start_server() {
  CONTAINER="$(docker run -d -p 127.0.0.1:0:3000 "$@" "${IMAGE}")"
  PORT="$(docker port "${CONTAINER}" 3000/tcp | head -n1 | sed 's/.*://')"
  for _ in $(seq 1 60); do
    if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  docker logs "${CONTAINER}" >&2 || true
  fail "/healthz never became healthy"
}

# drop_server removes the container started by start_server.
drop_server() {
  docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
  CONTAINER=""
}

step "image is configured to run as the numeric user 1000:1000"
# Numeric, so Kubernetes runAsNonRoot can verify it without an explicit runAsUser.
expect_eq "$(docker inspect -f '{{.Config.User}}' "${IMAGE}")" "1000:1000" "Config.User"

step "process runs as uid 1000 and HOME is /home/athens"
expect_eq "$(docker run --rm --entrypoint id "${IMAGE}" -u)" "1000" "uid"
expect_eq "$(docker run --rm --entrypoint sh "${IMAGE}" -c 'echo "$HOME"')" "/home/athens" "HOME"

step "running as root (the documented rollback) gets HOME=/root, not /home/athens"
# The image must not hard-code HOME, or runAsUser: 0 would ignore /root mounts.
expect_eq "$(docker run --rm --user 0 --entrypoint sh "${IMAGE}" -c 'echo "$HOME"')" "/root" "HOME as root"

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

step "a root-owned bind mount on /var/lib/athens is NOT writable (documented breaking case)"
# Create the directory as root so the result does not depend on the uid running this script.
docker run --rm --user 0 -v "${WORK}:/data" --entrypoint sh "${IMAGE}" -c \
  'mkdir /data/rootowned && chown 0:0 /data/rootowned && chmod 755 /data/rootowned'
if docker run --rm -v "${WORK}/rootowned:/var/lib/athens" --entrypoint sh "${IMAGE}" -c 'touch /var/lib/athens/probe' 2>/dev/null; then
  fail "root-owned bind mount was writable; the docs say it must be chowned to 1000:1000"
fi

step "an arbitrary uid in group 0 (OpenShift style) can write HOME and the storage root"
docker run --rm --user 12345:0 -e HOME=/home/athens --entrypoint sh "${IMAGE}" -c \
  'touch /home/athens/probe /var/lib/athens/probe' \
  || fail "arbitrary uid with gid 0 cannot write /home/athens or /var/lib/athens"

step "a mounted .netrc owned by uid 1000 is readable"
printf 'machine example.com login u password p\n' > "${WORK}/netrc"
docker run --rm --user 0 -v "${WORK}:/data" --entrypoint sh "${IMAGE}" -c \
  'chown 1000:1000 /data/netrc && chmod 600 /data/netrc'
docker run --rm -v "${WORK}/netrc:/home/athens/.netrc:ro" --entrypoint sh "${IMAGE}" -c 'test -r /home/athens/.netrc' \
  || fail "mounted .netrc is not readable"

step "ATHENS_NETRC_PATH: Athens copies the .netrc into HOME"
start_server -v "${WORK}/netrc:/etc/athens/netrc:ro" \
  -e ATHENS_NETRC_PATH=/etc/athens/netrc -e ATHENS_STORAGE_TYPE=memory
docker exec "${CONTAINER}" grep -q 'machine example.com' /home/athens/.netrc \
  || fail "Athens did not write /home/athens/.netrc from ATHENS_NETRC_PATH"
drop_server

step "ATHENS_GITHUB_TOKEN: Athens writes a .netrc into HOME"
start_server -e ATHENS_GITHUB_TOKEN=ghp_smoke -e ATHENS_STORAGE_TYPE=memory
docker exec "${CONTAINER}" grep -q 'machine github.com login ghp_smoke' /home/athens/.netrc \
  || fail "Athens did not write /home/athens/.netrc from ATHENS_GITHUB_TOKEN"
drop_server

step "an arbitrary uid in group 0 can start the server and write its .netrc"
start_server --user 12345:0 -e HOME=/home/athens \
  -e ATHENS_GITHUB_TOKEN=ghp_smoke -e ATHENS_STORAGE_TYPE=memory
docker exec "${CONTAINER}" grep -q 'machine github.com login ghp_smoke' /home/athens/.netrc \
  || fail "arbitrary uid could not write /home/athens/.netrc"
drop_server

step "a module is fetched through the proxy and stored on the named volume (go and git run as uid 1000)"
start_server -v "${VOLUME}:/var/lib/athens" \
  -e ATHENS_STORAGE_TYPE=disk -e ATHENS_DISK_STORAGE_ROOT=/var/lib/athens
MOD="http://127.0.0.1:${PORT}/github.com/stretchr/testify/@v/v1.9.0"
fetched=""
for _ in 1 2 3; do
  if curl -fsS --max-time 120 "${MOD}.info" >/dev/null && curl -fsS --max-time 120 -o "${WORK}/testify.zip" "${MOD}.zip"; then
    fetched=1
    break
  fi
  sleep 5
done
if [[ -z "${fetched}" ]]; then
  docker logs "${CONTAINER}" >&2 || true
  fail "could not fetch github.com/stretchr/testify@v1.9.0 through the proxy"
fi
docker run --rm -v "${VOLUME}:/var/lib/athens" --entrypoint sh "${IMAGE}" -c \
  'find /var/lib/athens -name "*.zip" | grep -q .' \
  || fail "fetched module was not stored on /var/lib/athens"
drop_server

step "the server starts, serves /healthz and exits 0 on SIGTERM"
start_server -e ATHENS_STORAGE_TYPE=memory
docker stop -t 30 "${CONTAINER}" >/dev/null
expect_eq "$(docker inspect -f '{{.State.ExitCode}}' "${CONTAINER}")" "0" "exit code after SIGTERM"

echo "OK: ${IMAGE}"
