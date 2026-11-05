# Screen Viewer

A Ludus plugin that shows a grid of view-only VM screens for every range you can access: your own, ones shared with you directly, and ones shared through a group. Select a screen to enlarge it. Refresh by hand or turn on auto refresh.

**How it captures a screen.** For each VM the backend checks access with `FindVM`, takes the caller's own Proxmox token from `ProxmoxCredentials`, calls Proxmox `vncproxy`, opens the `vncwebsocket`, joins VNC as a shared client, reads one frame, and returns it as a JPEG. It sends no keyboard or mouse input and stores nothing.

**Declared access.** `level: standard`, `runAs: ludus`, no forwarded credentials. The plugin process does briefly hold the caller's Proxmox token in memory, and it never returns the token to the browser. Activation runs trusted code with the `ludus` account's OS access.

**Things to know**

- Each capture makes Proxmox set a new temporary VNC password on the VM. People already on the console stay connected, but someone opening a console at the same moment may need to retry. Auto refresh is off by default for this reason.
- Administrators see every range in the list, but capture uses their own Proxmox token, so VMs it has no rights on show "Your Proxmox account cannot open this VM's console".
- VMs without a VGA display (serial console only) and containers show a capture failure rather than a screen.

```sh
./scripts/fetch-sdk.sh <your Ludus version>
PLUGIN_VERSION=0.1.0 ./scripts/build.sh .ci-deps/ludus/ludus-plugin-sdk dist
node --test scripts/test-ui.cjs
```

The rest of this file is the template's general build and release guide.

# Ludus plugin template

Build plugins for **Resources → Plugins** using the Go module `gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk`.

## Build a plugin with an AI coding agent

Copy this repository, open it in your agent, and ask for what you want, for example:

> Read AGENTS.md and build a Ludus plugin that shows each VM in the selected range with its IP and power state, and lets me power off the routers.

[AGENTS.md](AGENTS.md) tells the agent how to fetch the matching SDK, pick the smallest plugin shape, adapt `source/`, test, and build an installable ZIP. Review the declared level, run-as account, and routes before you activate the result.

## Make your plugin by hand

| File | What to change |
| --- | --- |
| [source/main.go](source/main.go) | `ludus.Plugin` identity, UI scope, routes, and HTTP handlers |
| [source/main_test.go](source/main_test.go) | Handler and permission tests using `ludustest` |
| [source/ui/index.html](source/ui/index.html) | Self-contained UI and `window.ludus` calls |

`ludus.Serve(declaration())` generates both runtime metadata and `plugin.json`; there is no second manifest to maintain. The plugin version is **not** in the source: `var version = "dev"` is replaced at link time with the release tag. The SDK uses **protocol version 1**, a different contract from the old RPC v2 API. The ZIP contains only `plugin.json`, the optional `plugin` executable, and optional `ui/index.html`; **do not package `openapi.json`** or anything else.

Read the [API contract](docs/PLUGIN_API.md), [SDK host-service guide](docs/LUDUS_API.md), and [UI guide](WEB_STYLE.md). They cover routes, scheduled jobs, browser apps (`App`), per-VM actions in the range editor (`VMActions`), API-scoped routes, VM hooks, and the browser bridge.

## Build and test

You need Python 3, Go 1.25 or newer, a C compiler for Go race tests, and the `ludus-plugin-sdk` directory from the public Ludus repository at the ref matching your server:

```sh
./scripts/fetch-sdk.sh    # or: ./scripts/fetch-sdk.sh <branch|tag|commit> to match your server

PLUGIN_VERSION=0.1.0 ./scripts/build.sh .ci-deps/ludus/ludus-plugin-sdk dist
# Target a Linux ARM64 server:
GOARCH=arm64 PLUGIN_VERSION=0.1.0 ./scripts/build.sh .ci-deps/ludus/ludus-plugin-sdk dist
```

Without `PLUGIN_VERSION`, the version is `git describe --tags --dirty` of your checkout; the build fails when neither is available. A Ludus checkout root also works as the SDK argument.

The script creates a temporary Go workspace containing only `source/` and the SDK module. It runs `go vet`, Go race tests, and package-validator tests; generates the manifest using a native executable; cross-compiles the Linux executable; and validates the ZIP. It never imports `ludusapi`, PocketBase, or the Ludus server/client modules.

Output is `dist/<id>-<version>.zip` and its `.sha256` file. Set `GOCACHE` to a writable directory in restricted environments.

Frontend tests use only Node.js built-in modules (Node 22 or newer):

```sh
node --test scripts/test-ui.cjs
```

These cover empty context, theme updates, range changes, stale responses, and retryable errors. Backend tests exercise the SDK over its RPC test harness, including host-denied access and a client-supplied range that must not override host context.

## Install and verify

Upload the ZIP under **Resources → Plugins**, or install it from a GitHub release URL (see below). An administrator reviews and activates every upload, including ordinary user uploads. The example declares `level: standard`, `runAs: ludus`, and no forwarded caller credentials. Activation runs trusted code with the declared account's OS access; a plugin process is not an OS sandbox.

Select a range and verify its machine list, loading/empty/error states, both themes, and access with the intended user roles. The host provides the plugin title and range selector; the example adds only its content panel.

## Publish on GitHub

`.github/workflows/release.yml` builds every push and pull request, runs the frontend tests, and saves the validated ZIP as a workflow artifact. Pushing an `X.Y.Z` tag (no `v` prefix) builds that version and publishes a GitHub release with the ZIP and SHA-256 checksums through GoReleaser.

```sh
git tag 1.0.0
git push origin 1.0.0
```

Ludus can then install it from `https://github.com/<owner>/<repo>` (latest release), `…/releases/tag/1.0.0`, or the ZIP asset URL. Each release must carry exactly one `.zip` asset for the repository/tag forms to work. The GitHub source archive is not an installable package.

| GitHub Actions variable | Purpose |
| --- | --- |
| `LUDUS_SDK_REF` | Ludus branch, tag, or commit providing the SDK. Unset (default) follows the newest Ludus version; set it to pin a server version |
| `LUDUS_PROJECT` | GitLab project path of the Ludus source (default `badsectorlabs/ludus`) |
| `LUDUS_GOARCH` | Linux server architecture: `amd64` (default) or `arm64` |

GitHub Actions must be enabled and its token allowed to create releases. UI-only builds skip SDK fetch and Go setup.

## UI-only plugins

For a plugin without a backend, create `source/plugin.json`:

```json
{
  "schemaVersion": 1,
  "id": "ludus-example",
  "name": "Ludus Example",
  "version": "0.0.0",
  "description": "A UI-only plugin",
  "author": "Your name",
  "ui": "ui/index.html",
  "rangeScoped": false
}
```

The presence of that file selects UI-only mode; the build replaces `version` with the tag or `PLUGIN_VERSION`. Delete the Go files, remove backend calls from the HTML, and adapt the frontend tests. Do not declare `executable`, routes, hooks, apps, or forwarded credentials. Build with `PLUGIN_VERSION=0.1.0 ./scripts/build.sh dist`; only Python is required. CI detects this mode automatically. Test the UI inside Ludus, which supplies `window.ludus`.

The visual guide is adapted from the Ludus GUI's `STYLE.md`. See [LICENSE](LICENSE).
