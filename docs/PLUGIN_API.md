# Ludus SDK plugin API

This template targets `gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk` (protocol version **1**), not the legacy `ludusapi/pluginrpc` interface. The SDK lives in the public Ludus repository (`https://gitlab.com/badsectorlabs/ludus`, directory `ludus-plugin-sdk/`); `scripts/fetch-sdk.sh` checks it out into `.ci-deps/ludus`. For anything not covered here, that checkout is authoritative: `ludus-plugin-sdk/{ludus.go,host.go,http.go,vmactions.go,install.go}`, `protocol/{manifest.go,protocol.go,host.go}`, and `ludustest/`. Host enforcement is in `ludus-api/plugin_packages.go`, `plugin_resources.go`, `plugin_apps.go`, and `plugin_host*.go`.

## Choose the plugin shape

Pick the smallest shape that delivers the feature:

| Need | Use |
| --- | --- |
| Only presentation of data the browser can already get, or calls to a user-approved external origin | UI-only package (`source/plugin.json`, no executable) |
| Read range/VM data, run work on the server, keep secrets server-side | Executable with `Routes` plus `ui/index.html` (the example) |
| Periodic work independent of any open browser | `Jobs` (runs as the plugin principal, not as a user) |
| A full browser application in its own tab (consoles, editors, WebSockets, streaming) | `App` plus a route that calls `Call.OpenApp` |
| A per-VM button in the GUI range editor that opens your app for that VM | `VMActions` (requires `App`) |
| A new endpoint under `/api/v2/...` for CLI/automation clients | `Scope: ludus.APIScope` route (system level only) |
| React to VM selection, start, stop, status, deletion, or address lookup | `VMHooks` + `VMHook` (system level and `runAs: root`) |
| Ship files into the Ludus install tree (roles, playbooks) | `ludus.InstallFiles` in `Init` (system level only) |

Prefer `standard` level and `ludus` run-as. Only escalate when a named feature needs it, and say why in the handoff.

## Package and identity

The ZIP contains **only** `plugin.json`, optional `plugin`, and optional `ui/index.html`, at the archive root, as regular files, with no directory entries. Anything else (including `openapi.json`, README files, extra assets) is rejected. The executable is a static Linux binary for the server's architecture. `ui/index.html` must inline all CSS, JavaScript, images, and fonts. At least one of UI or executable is required. Limits: ZIP and executable 128 MiB, manifest 64 KiB, UI 2 MiB.

Declare the backend once in `ludus.Plugin`. Running the binary with `--ludus-manifest` prints `plugin.json` without starting the RPC server or running `Init`; `scripts/build.sh` does this with a host-native build, then cross-compiles the Linux binary. Never hand-write `plugin.json` for an executable plugin.

The plugin **version comes from the release tag**: `source/main.go` declares `var version = "dev"`, and `scripts/build.sh` links `-X main.version=$PLUGIN_VERSION` (or `git describe --tags`). Do not hard-code a version in the declaration.

The manifest uses schema version 1. IDs match `^[a-z][a-z0-9-]{1,63}$`. Name (≤120 bytes) and version (≤64) are required; description ≤2000, author ≤200. Identity, level, run-as, forwarded credentials, app, VM actions, routes, and VM hook capabilities must agree with runtime metadata, or activation fails. Package entries must match declarations exactly: a packaged `ui/index.html` without `ui` (or vice versa) is rejected.

| Declaration | Manifest | Meaning |
| --- | --- | --- |
| `Level` | `level` | `ludus.Standard` (default) or `ludus.System`: Ludus capabilities |
| `RunAs` | `runAs` | `ludus.RunAsLudus` (default) or `ludus.RunAsRoot`: OS account |
| `UI: &ludus.UI{RangeScoped: true}` | `ui`, `rangeScoped` | Ship `ui/index.html`; host supplies and requires a selected range |
| `Routes` | `routes` | Exact names, methods, paths, scopes, and `http.HandlerFunc`s |
| `ForwardCallerCredentials` | `forwardCallerCredentials` | Keep caller `Authorization`/`X-API-KEY`/`Cookie` on routed requests; default false |
| `App` | `app` | `http.Handler` served as a browser app at `/plugin-apps/{resourceID}/{sessionID}/` |
| `AppSessionClosed` | — | Callback when an app session ends; requires `App` |
| `VMActions` | `vmActions` + two SDK routes | Per-VM app launches in the range editor; requires `App` |
| `Jobs` | — | Scheduled callbacks; unique names, interval ≥ 1 s |
| `VMHooks` / `VMHook` | `vmHooks` | VM lifecycle capabilities and callback; system level and root run-as |
| `Init`, `Shutdown` | — | Optional startup/cleanup callbacks |

Level and run-as are independent: root run-as does not grant system-level host methods, and system level does not grant OS root. Every upload and replacement requires administrator activation of the exact reviewed SHA-256, level, and run-as.

## Handler and lifecycle contract

Call `ludus.Serve(declaration())` in `main`. The SDK implements the wire protocol; plugins do not implement legacy `Metadata`, `Initialize`, `Handle`, or `RunJob(string)` methods.

| Callback | Runs as | Host service lifetime |
| --- | --- | --- |
| `Route.Handler(w, r)` | The request's caller | Until the handler returns |
| `Init(c, env) error` | Plugin principal | Until `Init` returns |
| `Job.Run(c) error` | Plugin principal | Until `Run` returns |
| `App` request (`ludus.CallFrom(r)`) | The session's caller | Until the app session ends; any goroutine |
| `VMActions.List/Launch(c, ...)` | The request's caller, `c.Range` resolved | Until the callback returns |
| `VMHook(c, request)` | Host VM operation | Until the hook returns |
| `Shutdown() error` | — | No host access |

`ludus.CallFrom(r)` supplies the authenticated principal (`Auth`), effective user (`User`), selected range (`Range`), environment (`Env`), and typed host service (`Host`). `User` can be nil (PocketBase superusers); `Range` can be nil. Validate the context your feature needs. Body, query, and headers are client input, not authorization.

Responses: `ludus.JSON(w, status, value)`, `ludus.Error(w, status, msg)` (`{"error":...}`), `ludus.Result(w, status, msg)` (`{"result":...}`), `ludus.HostError(w, err)` (maps host errors to 403/404/400/503/500). `ludus.DecodeJSON(r, &v)` decodes a body; bound its size with `http.MaxBytesReader` first. `ludus.Logger()` writes structured logs to stderr, which the host collects. The browser's `request` bridge expects JSON; `download` accepts any bytes.

See [SDK host-service access](LUDUS_API.md) for host methods, permissions, environment, state, and background work.

## Routes

A route has a unique `Name` and a unique scope/method/path. Methods: `GET`, `POST`, `PUT`, `PATCH`, `DELETE`. Patterns are exact absolute paths: no parameters, wildcards, queries, `..`, `//`, spaces, or braces. Put identifiers in the query or JSON body and validate them.

Default scope is `ludus.PluginScope`:

```text
/api/v2/plugins/{resourceID}/rpc/{pattern-without-leading-slash}
```

`{resourceID}` is the installation's record ID from `GET /api/v2/plugins`, not the plugin ID. The GUI bridge accepts only paths made of letters, digits, `_`, `-`, and single `/` separators, so use names such as `/status` or `/reports/export`.

The host checks plugin visibility, resolves the effective user and range, and validates access before your handler runs. With `rangeScoped: true` a selected accessible range is required. Host methods check permission again on every call. Use the resolved `c.Range.Number` with host methods, never a number from the body or query.

`Scope: ludus.APIScope` mounts a system plugin route at `/api/v2/{pattern}`. It must not start with `/plugins` and must not conflict with core or other active plugin routes. These routes are reachable by any authenticated caller regardless of plugin sharing, so the handler must authorize the caller itself.

## Browser applications (`App`)

Use an app when the iframe UI is too constrained: full-page layouts, WebSockets, streaming responses, or third-party web frontends. The host serves `App` on the Ludus origin at `/plugin-apps/{resourceID}/{sessionID}/`, authenticates every request with a session cookie, strips the prefix (your handler sees paths relative to `/`), and proxies streaming and WebSocket upgrades. `Call.AppBasePath` is the absolute prefix; use relative links in HTML or prefix absolute ones with it.

Sessions are opened from a request handler with `c.OpenApp(path, session)`; `path` is where the browser lands (`"/"` or `"/#/fragment"`). It returns a one-time `AppLaunch.URL` (valid ~30 s) and `AppLaunch.Session`, a `*Call` that stays valid from any goroutine until the session ends. The host ends sessions after an hour without renewal, when the caller's login expires, or when the caller loses access; then `AppSessionClosed(session)` runs. Defaults: 16 live sessions per user and 512 total; change with `Host.SetAppSessionLimits(perUser, total)`. `OpenApp` cannot be called from inside an app session.

```go
//go:embed app
var appFiles embed.FS

func declaration() *ludus.Plugin {
	site, _ := fs.Sub(appFiles, "app")
	return &ludus.Plugin{
		ID: "ludus-console", Name: "Console", Version: version,
		UI:     &ludus.UI{RangeScoped: true},
		App:    http.FileServerFS(site), // or any http.Handler / mux
		Routes: []ludus.Route{{Name: "OpenApp", Method: http.MethodPost, Pattern: "/open", Handler: openApp}},
	}
}

func openApp(w http.ResponseWriter, r *http.Request) {
	c := ludus.CallFrom(r)
	if c == nil || c.User == nil {
		ludus.Error(w, http.StatusForbidden, "A Ludus user is required")
		return
	}
	launch, err := c.OpenApp("/", "")
	if err != nil {
		ludus.HostError(w, err)
		return
	}
	ludus.JSON(w, http.StatusOK, map[string]string{"url": launch.URL})
}
```

In `ui/index.html`, feature-detect `context.capabilities.openApplication`, then `const {url} = await ludus.request('/open', {method: 'POST'}); await ludus.openApplication(url);`. The GUI only opens URLs under this plugin's own `/plugin-apps/{resourceID}/` prefix.

## Per-VM actions (`VMActions`)

`VMActions` adds buttons to VMs in the GUI range editor. The SDK serves `GET /vm-actions` and `POST /vm-actions/launch` for you (do not declare them yourself). `c.Range` is already access-checked. `Launch` must re-validate that `id` belongs to `c.Range`; it is client input.

```go
VMActions: &ludus.VMActions{
	List: func(c *ludus.Call) ([]ludus.VMAction, error) {
		vms, err := c.Host.RangeVMs(c.Range.Number)
		if err != nil {
			return nil, err
		}
		actions := []ludus.VMAction{}
		for _, vm := range vms {
			id := strconv.Itoa(vm.ProxmoxID)
			actions = append(actions, ludus.VMAction{ID: id, ProxmoxID: vm.ProxmoxID, Label: "Open console", Icon: "Terminal"})
		}
		return actions, nil
	},
	Launch: func(c *ludus.Call, id string) (*ludus.AppLaunch, error) {
		vms, err := c.Host.RangeVMs(c.Range.Number)
		if err != nil {
			return nil, err
		}
		for _, vm := range vms {
			if strconv.Itoa(vm.ProxmoxID) == id {
				return c.OpenApp("/#/vm/"+id, "")
			}
		}
		return nil, ludus.ErrNotFound
	},
},
```

`Icon` is an IBM Carbon icon name (`Terminal`, `Screen`, …); `IconSVG` is your own monochrome SVG drawn as a mask; `Color` is `#rgb`/`#rrggbb`. Host errors from `List`/`Launch` map through `HostError`; other errors become 409 with the message shown to the user.

## Resource management

Authenticated Ludus API paths under `/api/v2`:

| Endpoint | Purpose |
| --- | --- |
| `GET /plugins` | Visible installations; `?available=true` includes shared plugins hidden by the caller |
| `POST /plugins/install` | Raw ZIP body (not multipart), or JSON `{"githubURL":"https://github.com/owner/repo[/releases[/latest\|/tag/T\|/download/T/asset.zip]]"}`; optional `allUsers`, `replace`, `resourceID` query parameters |
| `POST /plugins/{resourceID}/add` | Add an available shared plugin back to the caller's list |
| `POST /plugins/{resourceID}/activate` | Administrator approves the reviewed package with `{"sha256":"...","level":"standard","runAs":"ludus"}` |
| `POST /plugins/{resourceID}/withdraw` | Stop and delete the root-owned copy of a package that is no longer approved |
| `PUT /plugins/{resourceID}/access` | Owner/admin sets `allUsers` and `allowedUsers` |
| `DELETE /plugins/{resourceID}` | Remove a shared plugin from the caller's list or delete an owned personal copy |
| `GET /plugins/{resourceID}/ui` | Active `{html, bridgeVersion}` frontend |
| `POST /plugins/{resourceID}/app/renew` | GUI renews the caller's live app sessions |
| `GET/POST/PUT/PATCH/DELETE /plugins/{resourceID}/rpc/{path}` | Declared plugin-scoped route |
| `GET /plugin-catalog`, `POST /plugin-catalog/{pluginID}/install` | Official, signed Bad Sector Labs catalog |

GitHub install picks the release's single `.zip` asset (multiple ZIP assets require the exact asset URL) and verifies GitHub's SHA-256 digest when present. Global uploads require an administrator. Each user can store up to 20 packages, with up to 5 pending approval. Activation must echo the exact SHA-256, level, and run-as reviewed; a changed package gets 409. Replacement preserves installation identity, ownership, and sharing but needs activation again. Deletion does not undo data or services a plugin created elsewhere.

For core HTTP operations, inspect the actual server API and implementation. The browser bridge does not grant arbitrary core API access.

## Browser bridge

Ludus loads `ui/index.html` in an opaque-origin iframe with `sandbox="allow-scripts allow-downloads"` and injects `window.ludus` (bridge version 1). Its CSP blocks network connections, external scripts/styles/fonts, and non-`data:` images; inline CSS/JS and WebAssembly work. The iframe cannot read parent DOM, storage, cookies, or auth tokens. Calls time out after 5 minutes.

| Method | Purpose |
| --- | --- |
| `await ludus.context()` | `{pluginID, rangeID, ranges: [{rangeID, name?}], capabilities: {openApplication, microphone}, theme, bridgeVersion}` |
| `await ludus.request(path, {method, query, body})` | JSON from this plugin's declared route |
| `await ludus.download(path, {query, filename})` | GET a declared route and save its bytes (≤64 MiB) |
| `await ludus.confirm(message)` | Host confirmation dialog; returns boolean |
| `await ludus.toast(message)` | Brief host notification |
| `await ludus.selectRange(rangeID)` | Request a range change from the host's accessible list |
| `await ludus.openApplication(url)` | Open this plugin's `App` launch URL in a new tab |
| `await ludus.networkFetch({url, method, headers, body})` | `{status, body, headers}` after per-origin user approval; string bodies |
| `await ludus.modelFetch({url, method, key, body})` | `networkFetch` adding Bearer authorization and JSON content type |
| `await ludus.microphone.start()` / `.stop()` | Approved microphone capture; samples arrive as `ludus:microphone` events |

`request('/status')` defaults to GET, JSON-serializes `body`, and expects JSON. Query values are strings, numbers, or booleans. The host adds authentication and the selected range; `rangeID`/`userID` query keys are rejected. A range-scoped plugin's `request`/`download` fail until a range is selected.

Read initial context and listen for `ludus:context` (`event.detail`). Merge updates so theme changes do not erase range state. On range change, clear old data and invalidate pending responses; do not refetch on theme-only changes. With no range, show an empty state and skip range requests.

The host supplies the plugin name and a default range selector. To put the range picker beside your own controls, render a labeled control marked `data-ludus-range-picker` (the host then hides its own selector), populate it from `context.ranges`, call `selectRange`, and wait for the next context event before loading.

Limits: 8 concurrent bridge calls, 8 MiB request bodies, 64 MiB downloads; network calls 8 MiB request / 16 MiB response, no redirects, no browser credentials, subject to CORS. Older GUIs also report bridge version 1 but lack newer methods: feature-detect `selectRange`, `download`, `openApplication`, `networkFetch`, `microphone`. Keep privileged credentials backend-only.
