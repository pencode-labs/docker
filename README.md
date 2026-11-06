# Docker for Pencode

A Docker container manager using Pencode's native extension UI and current theme.

- Click the container icon in the top-right title bar to list all containers in the current Docker context, including stopped containers.
- Start, stop or restart containers. Actions run in the background and refresh the list when complete.
- Click a container to open its logs in the bottom panel, with timestamps and both stdout and stderr.
- Lists and visible logs refresh every three seconds. Logs show the latest 300 lines, bounded to 256 KiB. Auto-scroll follows new output until you scroll up; pause automatic refresh to inspect or copy logs.
- Hidden or closed surfaces stop polling; closing a surface cancels its pending read. An already requested start/stop/restart continues until it completes or the extension shuts down.

## Build and install

Requires Go 1.27 or newer, Pencode with the matching PEB SDK, and Docker CLI on the **workspace server**. The extension uses that server's Docker context, credentials and environment (`DOCKER_HOST`, `DOCKER_CONTEXT`, etc.). Docker Desktop is supported through its CLI. The Docker daemon must be running and accessible to the Pencode server user.

```sh
git clone https://github.com/pencode-labs/docker.git
cd docker
go build -o bin/docker .
```

In Pencode's extension manager, choose **Install from local directory** and select this checkout. Build for the workspace server's OS and architecture when using a remote workspace.

For Windows, build `bin/docker.exe` and update `entry` in `resources/extension.json` to `bin/docker.exe`. Release packages set the correct entry automatically.

## Development

```sh
go test -race ./...
go vet ./...
fieldalignment ./...
```

Tests use a fake CLI to cover process execution, stderr logs, cancellation, output limits and UI lifecycle without touching real containers.

Push a `v*` tag to build Linux, macOS and Windows release archives for amd64 and arm64, with checksums, using the same release workflow as other Pencode extensions.

## License

MIT. The container icon is from Lucide under its ISC license; see `resources/LUCIDE-LICENSE.txt`.
