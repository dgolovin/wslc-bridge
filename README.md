# WSLC Docker Engine Bridge

`wslc-bridge.exe` exposes a Docker-compatible HTTP API on a Windows named pipe and bridges it to Microsoft's WSL Containers (WSLC) runtime. It uses the guest Docker daemon for API reads and calls `wslc.exe` directly for operations that need Windows port relay or Windows host binds.

The bridge is an independent executable. It does not require a Windows Docker daemon and does not install, update, or stop WSLC. The Podman Desktop extension that will bundle and manage this executable is a separate follow-up milestone.

## Requirements

- Windows 10 or 11. Windows Server 2025 has community testing; Windows Server 2022 is not a supported target for this v1 release.
- WSLC CLI available as `wslc.exe` on `PATH`.
- WSL 2.9.3 or newer. If WSL is older, update it in an elevated PowerShell terminal:

  ```powershell
  wsl --update
  ```

- The default WSLC guest session must have Docker CLI and daemon available. This bridge uses:

  ```powershell
  wslc.exe system session run docker -H unix:///var/run/docker.sock system dial-stdio
  ```

  The guest transport was verified against Docker CLI 25.0.7 and daemon 25.0.3 (API 1.44).

## Build

The build scripts produce Windows x64 and ARM64 executables and a SHA-256 manifest. ARM64 is cross-built; validate it on ARM64 Windows hardware before claiming runtime support.

From PowerShell:

```powershell
./scripts/build.ps1
```

From macOS/Linux with Go 1.26 or newer:

```bash
bash scripts/build.sh
```

Artifacts are written to `dist/`:

```text
wslc-bridge-windows-amd64.exe
wslc-bridge-windows-arm64.exe
bridge-manifest.json
checksums.txt
```

The build scripts pin `github.com/Microsoft/go-winio` at v0.6.3 through `go.mod`/`go.sum`, use CGO-disabled cross-builds, and embed version/commit/build-date metadata.

## Run

Check runtime prerequisites and the guest Docker API before starting:

```powershell
./dist/wslc-bridge-windows-amd64.exe doctor --json
```

Start the Windows named-pipe server:

```powershell
./dist/wslc-bridge-windows-amd64.exe serve
```

By default, the pipe is:

```text
\\.\pipe\wslc_engine
```

Use a private pipe name for local testing:

```powershell
./dist/wslc-bridge-windows-amd64.exe serve --pipe '\\.\pipe\wslc_engine_test'
```

The pipe ACL grants access to the Windows user running the bridge; remote pipe clients are rejected. A bridge process already owning the name causes a startup error. `serve` writes one JSON readiness event to stdout after runtime checks and listener startup; diagnostics go to stderr. Ctrl+C shuts down the listener and bridge-owned guest transport processes without stopping WSLC or containers.

For clients that support Windows named pipes, configure:

```powershell
$env:DOCKER_HOST = 'npipe:////./pipe/wslc_engine'
docker version
docker ps --all
```

The Docker CLI is only a client for this test; it does not need a running Docker Desktop/Engine. Podman Desktop will register this pipe directly as its `WSL Containers` Docker-type provider connection in the follow-up extension milestone.

## Implemented API

The bridge supports unversioned Docker API paths and versioned paths from **1.24 onward**; the guest daemon enforces its own maximum API version. API reads are served by the WSLC guest daemon, except container port data, which is merged from `wslc container list --all --format json`.

Implemented reads:

- `/_ping`, `/version`, `/info`, `/events`
- `/containers/json`, `/containers/{id}/json`, `/containers/{id}/logs`, `/containers/{id}/stats`
- `/images/json`, `/images/{reference}/json`
- `/networks`, `/networks/{id}`
- `/volumes`, `/volumes/{name}`

Implemented mutations:

- `/images/create` for anonymous/public pulls.
- `/containers/create`, `/containers/{id}/start`, `/containers/{id}/stop`, and `DELETE /containers/{id}`.
- `/networks/create` and `DELETE /networks/{id}`.
- Non-interactive exec through Docker's exec create/start/inspect calls.

Container create is deliberately allow-listed. It maps image, command/args, name, explicit environment values, labels, user, working directory, single-value entrypoint, supported networks, TCP/UDP published ports, Windows/named binds with `ro` or `rw`, tmpfs, and selected resource settings. Requests that ask for unsupported semantics are rejected rather than silently discarded.

WSLC native inspect supplies the Windows host binding address and allocated port. The bridge overlays that data into both Docker inspect's `NetworkSettings.Ports` and container-list `Ports`; a guest VM port is never reported as a Windows-published port. Native list output is decoded across the 2.9.3 JSON-array and newer JSON-lines formats.

## Limitations

- Private-registry credentials, all-tags/platform-specific image pulls, and imports are not supported. Anonymous public pulls are supported.
- WSLC 2.9.3 limitations apply: no CLI `--mount` parser, static IPs, host networking, custom exposed-only ports, or full Docker HostConfig parity. Unsupported requested options return an error.
- WSLC create can implicitly pull an image missing between the bridge's image preflight and native create.
- Exec is synchronous and non-interactive only; exec environment/user/workdir overrides, TTY, attached stdin, and detached exec are unsupported.
- Compose, Docker-in-Docker, host Docker socket mounts, privileged mode, restart policy, `host.docker.internal`, and `--add-host` are out of scope.
- This is partial Docker Engine API compatibility, not a general Docker Engine replacement. Unimplemented API routes return HTTP 501.
- The WSL Containers extension stages and launches this bridge when the pinned artifacts are available. Run `serve` manually only when diagnosing the standalone bridge independently of Podman Desktop.

## Tests and validation

Run automated checks on the development host:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Cross-build both Windows architectures:

```bash
bash scripts/build.sh
```

On Windows, run the real WSLC smoke test after starting the bridge or activating the extension. The test sends Docker HTTP requests directly over the named pipe using PowerShell; it does not require `docker.exe`, `podman.exe`, or a separate container engine:

```powershell
./scripts/smoke-test.ps1
```

The smoke test performs a public nginx pull, container create/start, Windows port and bind checks, logs/stats/non-interactive exec, and network create/inspect/remove. It removes its test container/network and temporary bind directory on completion.

Unit tests do not require WSLC. Windows CI runs the named-pipe test under the current user; the real WSLC integration and ARM64 runtime still require hardware validation.
