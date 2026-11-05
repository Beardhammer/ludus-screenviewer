# Using the Ludus plugin SDK

Import `ludus "gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk"`. Do not import `ludusapi`, its models, or PocketBase. The SDK does not give plugins a database token or load `config.yml`.

The request flow is:

`window.ludus.request('/status')` → authenticated Ludus dispatcher → SDK HTTP handler → `ludus.CallFrom(r).Host`.

## Caller and range context

Handlers are ordinary `func(http.ResponseWriter, *http.Request)` functions. The SDK attaches a `*ludus.Call` to the request:

| Field | Meaning |
| --- | --- |
| `Auth` | Authenticated principal; contains `RecordID`, `UserID`, `Name`, `IsAdmin`, `IsSuperuser` |
| `User` | Effective user, possibly different when an administrator acts as another user; nil for PocketBase superusers and outside HTTP requests |
| `Range` | Host-resolved selected range; nil without range context |
| `Host` | Typed host service bound to this call and its authorization |
| `Env` | Read-only plugin environment from initialization |
| `ID` | Call ID; expires when the handler, job, or hook returns (app sessions: when the session ends) |
| `AppSession`, `AppBasePath` | Set on `App` requests and `AppLaunch.Session` |

`c.IsAdmin()` checks the authenticated caller, not the effective user's flags. `Range.RecordID` is a database record ID, `Range.RangeID` is the human-facing Ludus ID, and `Range.Number` is the integer used by host methods. They are not interchangeable. The ROOT pseudo-range has an empty record ID; features requiring a real range must reject it.

The [working example](../source/main.go) checks for an ordinary Ludus user and a real range, calls `c.Host.RangeVMs(c.Range.Number)`, and returns only VM names. It deliberately ignores range/user query parameters. The dispatcher resolves access; the host service enforces permission again for every operation.

## Host operations

Signatures and data fields are in the SDK's `host.go` and `protocol/`. Enforcement is in the host's `ludus-api/plugin_host_service.go` (`authorize`) and `plugin_host_methods.go`. "Request" below means an HTTP route handler or `VMActions` callback; `Init` and jobs run as the plugin principal.

| Need | SDK operation | Who may call |
| --- | --- | --- |
| Caller-accessible ranges | `AccessibleRanges()`, `CanAccessRange(number)` | Request |
| VM inventory of a range | `RangeVMs(number)` | Request; system plugin principal |
| Find a VM and its range by Proxmox ID | `FindVM(proxmoxID)` | Request |
| Range config / Ansible log | `ReadRangeFile(number, protocol.RangeFileConfig\|RangeFileAnsibleLog, maxBytes)` | Request |
| Ludus log history | `Logs(ludus.LogsQuery{...})` | Request |
| Run Ansible with host logging | `RunPlaybook(ludus.Playbook{Root: protocol.PlaybookRootStateDir, Playbook: "x.yml", ExtraVars: ...})` | Request (`range-management` root: system only) |
| Caller's own Proxmox credentials | `ProxmoxCredentials()` | Request |
| Range inactivity override / activity | `SetRangeInactivityTimeout(number, timeout)`, `TouchRange(number)` | Request |
| License state | `License()` | Anyone |
| Browser app sessions | `Call.OpenApp(path, session)` | Request (not from an app session) |
| | `EndAppSession()` | App session calls |
| | `SetAppSessionLimits(perUser, total)` | Any call of an `App` plugin |
| Quota status of a user | `QuotaStatus(userID)` | Request to a system plugin |
| Quota administration | `ListQuotaStatus`, `ListGroupQuotas`, `SetUserQuota`, `SetGroupDefaultQuota` | Request to a system plugin from an admin |
| Machine credentials directory | `MachineCredentialsDir(number)` | Request to a system plugin |
| All ranges, power off VMs, root Proxmox credentials | `ListRanges`, `PowerOffVMs`, `RootProxmoxCredentials` | System plugin principal, or request from an admin to a system plugin |

Range-addressed methods also check that the caller can access that range (admins and system plugin principals may address any range). Setting `runAs: root` does not grant system-level host methods. Inspect the host implementation before designing a privileged feature.

`RangeVMs` returns Ludus's recorded inventory (`Name`, `IP`, `ProxmoxID`, `PoweredOn`, `IsRouter`, `Template`, `OS`, …); it does not refresh live power state or addresses. Host errors support `errors.Is(err, ludus.ErrForbidden)` and `ErrNotFound`, `ErrInvalid`, `ErrExpired`, `ErrUnavailable`. `ludus.HostError(w, err)` maps them to 403, 404, 400, 503, and 503; other errors to 500.

## Initialization, jobs, and state

Add `Init: func(c *ludus.Call, env ludus.Env) error` when needed. The environment includes `ResourceID`, `LudusVersion`, `Level`, `RunAs`, `Source`, `StateDir`, `InstallPath`, explicit non-secret `Settings` (storage pools, NAT/DNS addresses, Proxmox node, WireGuard endpoint, default quotas, …), `License`, and the local `API` endpoint. It does not contain a database connection or root Proxmox token.

Use `env.StateDir` (`<data_directory>/plugin-state/<plugin id>`, mode 0700, owned by the run-as user) for persistent data and for playbooks run with `PlaybookRootStateDir`. `ludus.InstallFiles(env, fsys...)` writes embedded files into the install tree and is system-level only. Scheduled `ludus.Job` callbacks run as the plugin principal, not as the user who last opened the UI; a standard plugin's job can call little more than `License`. Each job needs a unique name, a `Run` function, and an interval of at least one second.

**Do not retain a `Call`, its `Host`, or its call ID for a background goroutine.** Authorization expires when the callback returns. Keep host operations within the call, use a job with appropriate system privileges, or design a fresh authenticated request for queued work. The one exception is `AppLaunch.Session` and app-request calls, which stay valid until the app session ends. Scope stored results to the owner and range and recheck access before returning them.

## Calling the HTTP API as a user

Prefer host methods. If a feature must call a core Ludus HTTP endpoint, declare `ForwardCallerCredentials: true`. The administrator sees this declaration; otherwise the host removes `Authorization`, `X-API-KEY`, and `Cookie` from plugin requests.

Construct the client with `ludus.APIClient(env.API)` to use the host's loopback endpoint and pinned TLS certificate. Forward only the caller credentials required by the operation, keep calls on that endpoint, and never expose them to the frontend or logs. The helper creates an HTTP client; it does not authenticate a request or grant extra permissions. Consult the actual server API/source (`ludus-api/`, `/api/v2/openapi` on a server) for paths and authorization. The browser bridge cannot call arbitrary core API routes.

## Tests

`ludustest.Start(t, declaration(), fakeHost, env)` initializes the plugin and connects a `ludustest.Host` over real in-process RPC.

| Harness | Use |
| --- | --- |
| `h.Do(method, pattern, opts...)` | Call a declared route; options `WithAuth`, `WithUser`, `WithRange`, `WithQuery`, `WithJSON`, `WithBody`, `WithHeader` |
| `h.RunJob(name)` | Run a scheduled job once |
| `h.App(protocol.AppContext{...})` | `http.Handler` for the plugin's `App` within a fake session |
| `h.CloseAppSession(session)` | Trigger `AppSessionClosed` |
| `h.VMHook(request)` | Invoke the VM hook |
| `ludustest.Host{RangeVMsFunc: ..., OpenAppFunc: ...}` | Fake host; unset methods return unavailable; return `protocol.HostErrorf(code, ...)` for denials |

This tests plugin behavior and host-error handling, not the real server's access policy. Keep live checks for activation, actual permissions, and GUI integration. See [source/main_test.go](../source/main_test.go) for missing context, spoofed query scope, denied/expired access, and empty inventory cases.
