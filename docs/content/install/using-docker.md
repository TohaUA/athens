---
title: Using the Athens Docker images
description: Information about Athens' Docker images
weight: 1
---

Whether setting Athens up using [Kubernetes](install/install-on-kubernetes/) or using the [Walkthrough](/Walkthrough), you'll most likely be using one of the images that the Athens project produces. This document details what images are available, and has a recap from the Walkthrough of how to use them on their own.

---

## Available Docker images

The Athens project produces two docker images, available via [Docker Hub](https://hub.docker.com/) 

1. A release version as [`gomods/athens`](https://hub.docker.com/r/gomods/athens), each tag corresponds with an Athens [release](https://github.com/gomods/athens/releases), e.g. `v0.7.1`. Additionally, a `canary` tag is available and tracks each commit to `main`
2. A tip version, as [`gomods/athens-dev`](https://hub.docker.com/r/gomods/athens-dev), tagged with every commit to `main`, e.g. `1573339`

For a detailed tags list, check each image's Docker Hub

## Running Athens as a Docker image

This is a quick recap of the [Walkthrough](/walkthrough)

**Run Athens with an init process.** Athens shells out to the Go toolchain (`go`, `git`,
`ssh`), and the orphaned processes those leave behind must be reaped by PID 1. The official
`gomods/athens` images run [tini](https://github.com/krallin/tini) as their entrypoint for
this, so the commands below work as-is. If you run Athens any other way, such as a custom
image that overrides the entrypoint or the `athens-proxy` binary on its own, run it under an
init: `tini`, `docker`/`podman run --init`, or systemd. Without an init, orphaned processes
pile up as zombies and requests may fail. See [`init` issues](#init-issues) for details.

### Using the `docker` cli

In order to run the Athens Proxy using docker, we need first to create a directory that will store the persistant modules.
In the example below, the new directory is named `athens-storage` and is located in our userspace (i.e. `$HOME`). 
Then we need to set the `ATHENS_STORAGE_TYPE` and `ATHENS_DISK_STORAGE_ROOT` environment variables when we run the Docker container.

**Bash**
```bash
export ATHENS_STORAGE=$HOME/athens-storage
mkdir -p $ATHENS_STORAGE
docker run -d -v $ATHENS_STORAGE:/var/lib/athens \
   -e ATHENS_DISK_STORAGE_ROOT=/var/lib/athens \
   -e ATHENS_STORAGE_TYPE=disk \
   --name athens-proxy \
   --restart always \
   -p 3000:3000 \
   gomods/athens:latest
```

**PowerShell**
```PowerShell
$env:ATHENS_STORAGE = "$(Join-Path $HOME athens-storage)"
md -Path $env:ATHENS_STORAGE
docker run -d -v "$($env:ATHENS_STORAGE):/var/lib/athens" `
   -e ATHENS_DISK_STORAGE_ROOT=/var/lib/athens `
   -e ATHENS_STORAGE_TYPE=disk `
   --name athens-proxy `
   --restart always `
   -p 3000:3000 `
   gomods/athens:latest
```

## Running as a non-root user

The Athens Docker image runs as the non-root user `athens` (`uid: 1000`, `gid: 1000`) with the home
directory `/home/athens`. The proxy and the `go`, `git`, `hg` and `ssh` processes it starts never run as root.
You do not need to pass `-u`.

> **Breaking change for existing deployments.** Older images ran as root and read credentials from
> `/root/`. Mount credential files under `/home/athens/` instead, for example `/home/athens/.netrc`,
> `/home/athens/.gitconfig`, `/home/athens/.ssh` and `/home/athens/.hgrc`.

```shell
docker run -d -v $ATHENS_STORAGE:/var/lib/athens \
   -e ATHENS_DISK_STORAGE_ROOT=/var/lib/athens \
   -e ATHENS_STORAGE_TYPE=disk \
   -v "$PWD/gitconfig/.gitconfig:/home/athens/.gitconfig:ro" \
   --name athens-proxy \
   --restart always \
   -p 3000:3000 \
   gomods/athens:latest
```

**Mounted files must be readable by the `athens` user.** A file or directory bind-mounted from the host keeps
its host owner and permissions, so a `0600` file owned by your own user is not readable inside the container.
Find the uid and gid of the image's user with:

```shell
docker run --rm --entrypoint id gomods/athens:latest
# uid=1000(athens) gid=1000(athens) groups=1000(athens)
```

Then make the mounted files readable by that uid, for example:

```shell
chown 1000:1000 gitconfig/.gitconfig
chown -R 1000:1000 ssh-keys && chmod 700 ssh-keys && chmod 600 ssh-keys/id_rsa
```

`ssh` refuses private keys that are accessible by other users, so keep keys at `0600` and owned by uid `1000`.
For secrets mounted in Kubernetes, set `securityContext.fsGroup: 1000` (or `runAsUser: 1000`) and a `defaultMode`
such as `0440` so the files are readable by the `athens` user.

**Volumes created by an older image are owned by root.** The image pre-creates `/var/lib/athens` owned by
`athens`, so a new Docker named volume mounted there is writable out of the box. A bind mount, or a volume that
was populated by a previous root-running container, keeps its old owner and Athens will fail to write to it.
Hand it to the `athens` user once:

```shell
# bind mount on the host
sudo chown -R 1000:1000 "$ATHENS_STORAGE"

# existing named volume
docker run --rm -u 0 -v athens-storage:/var/lib/athens --entrypoint chown \
   gomods/athens:latest -R 1000:1000 /var/lib/athens
```

If you must keep running as root, for instance while migrating, pass `-u 0 -e HOME=/root` and keep using the `/root/` paths. Running as root is not recommended.

## Troubleshooting Athens in Docker

### `init` issues

The Athens docker image uses [tini](https://github.com/krallin/tini) so that defunct processes get reaped.
Docker 1.13 and greater includes `tini` and lets you enable it by passing the `--init` flag to `docker run` or by configuring the docker deamon with `"init": true`. When running in this mode. you may see a warning like this:

```console
[WARN  tini (6)] Tini is not running as PID 1 and isn't registered as a child subreaper.
 Zombie processes will not be re-parented to Tini, so zombie reaping won't work.
 To fix the problem, use the -s option or set the environment variable TINI_SUBREAPER to register Tini as a child subreaper, or run Tini as PID 1.
```
This is the "Athens-tini" complaining that it's not running as PID 1.
There is no harm in that, since the zombie processes will be reaped by the `tini` included in Docker.

Athens does not reap processes itself. Orphaned toolchain processes are reaped by whatever
runs as PID 1, which is `tini` in the official image. If you bypass the tini entrypoint or
run the binary directly as PID 1, provide an init (`--init`, `"init": true`, or `tini`), or
orphaned processes accumulate as zombies over time. See the recommendation under
[Running Athens as a Docker image](#running-athens-as-a-docker-image).
