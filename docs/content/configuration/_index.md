---
title: "Configuring Athens"
date: 2018-10-16T12:14:01-07:00
weight: 3
---

## Configuring Athens
Here we'll cover how to configure the Athens application utilizing various configuration scenarios.

>This section covers some of the more commonly used configuration variables, but there are more! If you want to see all the configuration variables you can set, we've documented them all in [this configuration file](https://github.com/gomods/athens/blob/main/config.dev.toml).

### Profiling (pprof)
Athens can expose the Go `pprof` endpoints with `EnablePprof` (`ATHENS_ENABLE_PPROF`) on the address set by `PprofPort` (`ATHENS_PPROF_PORT`). It is off by default.

> **Warning:** pprof has no authentication, and heap and goroutine profiles can expose secrets held in memory, such as credentials and tokens. The default `PprofPort` of `:3001` listens on all network interfaces. Bind it to loopback instead (for example `127.0.0.1:3001`) or restrict network access to it, never expose it publicly, and disable it again when you are done.

In the Docker image a loopback address is only reachable from inside the container. For example, with `PprofPort` set to `127.0.0.1:3001`, run `docker exec athens-proxy wget -qO- http://127.0.0.1:3001/debug/pprof/` (the image has BusyBox `wget` but no `curl`), or use `kubectl port-forward` in Kubernetes. Do not publish the pprof port with `-p`.

### Authentication
There are numerous version control systems available to us as developers.  In this section we'll outline how they can be used by supplying required credentials in various formats for the Athens project.

 - [Authentication](/configuration/authentication)
 
### Storage
In Athens we support many storage options. In this section we'll describe how they can be configured

 - [Storage](/configuration/storage)

### Upstream proxy
In this section we'll describe how the upstream proxy can be configured to fetch all modules from a Go Modules Repository such as [GoCenter](https://gocenter.io), [The Go Module Mirror](https://proxy.golang.org), or another Athens Server.

  - [Upstream](/configuration/upstream)

### Proxying A Checksum DB
In this section we'll describe how to proxy a Checksum DB as per https://go.googlesource.com/proposal/+/master/design/25530-sumdb.md

- [Checksum](/configuration/sumdb)

### Proxying Go toolchain downloads
In this section we'll describe how Athens can also serve the Go release archives from https://go.dev/dl, so that tools such as `actions/setup-go` download the Go toolchain itself through Athens.

- [Go downloads](/configuration/go-download)
