# Instructions for coding agents

This repository is a template for **Ludus SDK plugins** (Resources → Plugins). A user will typically say "read this repo and build a Ludus plugin that does X". Turn that into a working, installable ZIP by adapting `source/`.

Read, in order: this file, [README.md](README.md), [docs/PLUGIN_API.md](docs/PLUGIN_API.md) (package, routes, apps, VM actions, browser bridge), [docs/LUDUS_API.md](docs/LUDUS_API.md) (host service, permissions, tests), and [WEB_STYLE.md](WEB_STYLE.md) for UI work. Then read `source/main.go`, `source/main_test.go`, and `source/ui/index.html`: they are the working reference for every new plugin.

## Get the SDK first

Backend plugins build against the SDK source in the public Ludus repository. Fetch it into the ignored `.ci-deps/` directory exactly as CI does. With no argument the script follows the newest Ludus version; pass a branch, tag, or commit to match a specific server:

```sh
./scripts/fetch-sdk.sh            # prints .ci-deps/ludus/ludus-plugin-sdk
./scripts/fetch-sdk.sh <ref>      # only when the user names a Ludus version
```

Treat `.ci-deps/ludus/ludus-plugin-sdk/` (`ludus.go`, `host.go`, `http.go`, `vmactions.go`, `protocol/`, `ludustest/`) as the API contract and `.ci-deps/ludus/ludus-api/plugin_*.go` as the host's enforcement. If the docs here and that source disagree, the source wins; mention the difference in your handoff.

## Workflow

1. **Design.** State the plugin's purpose, who uses it, whether it is range-scoped, what data it reads or changes, and where that data comes from. Pick the smallest shape from "Choose the plugin shape" in `docs/PLUGIN_API.md`. Map each user action to a host method or a declared route. If a needed capability does not exist in the SDK host service or a documented Ludus API, say so instead of inventing it.
2. **Identity.** In `source/main.go`, set `ID` (`^[a-z][a-z0-9-]{1,63}$`, usually `ludus-<name>`), `Name`, `Description`, `Author`. Keep `Version: version`; the release tag supplies it. Rename the Go module in `source/go.mod` if you like; do not add an SDK `require` (the build's Go workspace supplies it).
3. **Backend.** Declare routes, jobs, app, and VM actions once in `ludus.Plugin`. Use standard `net/http` handlers and `ludus.Serve`. Split into more files in `source/` when it grows; keep `package main`.
4. **Authorize every handler** from `ludus.CallFrom(r)`: check `c.User`, `c.Range` as needed, and pass `c.Range.Number` to host methods. Client-supplied IDs, range numbers, and user IDs are never authorization; re-validate any ID against host data from this call. Bound request bodies. Keep credentials backend-only. Do not retain a `Call` or its `Host` after the callback returns (app sessions excepted).
5. **UI.** Keep `source/ui/index.html` self-contained (inline CSS/JS, no external assets or network) and use `window.ludus`. Support theme and range updates, keyboard focus, loading/empty/error/success states, and stale-response protection, as the example does. Do not repeat the host's plugin title or navigation.
6. **Tests.** Extend `source/main_test.go` with `ludustest`: success, missing user/range, spoofed query scope, invalid input, and host denials for each route. Update `scripts/test-ui.cjs` when you change the UI's DOM or behavior.
7. **Build.** Run `PLUGIN_VERSION=0.1.0 ./scripts/build.sh .ci-deps/ludus/ludus-plugin-sdk dist` (vet, race tests, manifest generation, Linux cross-compile, package validation) and `node --test scripts/test-ui.cjs`. Fix every failure; do not weaken the validator or tests to pass.
8. **Handoff.** Report the ZIP path, the declared level/run-as/forwarded credentials/app, the checks you ran, and what remains unverified on a live Ludus server. Explain that activation needs an administrator and runs trusted code with the declared OS account's access.

## Constraints

- Do not import `ludusapi`/PocketBase, open the Ludus database, or read host configuration. Use the host service and `env.Settings`.
- Default to `standard` level, `ludus` run-as, no forwarded credentials. Escalate only for a named need: system level for API-scoped routes, system host methods, `InstallFiles`, or the `range-management` playbook root; root run-as for OS-level work and VM hooks (which also need system level).
- The ZIP holds only `plugin.json`, `plugin`, and `ui/index.html`. Embed any other assets (playbooks, web app files) in the binary with `//go:embed`; write playbooks into `env.StateDir` before `RunPlaybook`.
- Do not hand-write `plugin.json` for an executable plugin. `source/plugin.json` exists only for UI-only plugins.
- Do not invent endpoints, host methods, bridge methods, or manifest fields. Check the SDK and host source.
- Never edit the Ludus host repository as part of implementing a plugin unless the user asks for a host change.
