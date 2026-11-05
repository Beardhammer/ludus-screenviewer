#!/usr/bin/env python3
"""Offline SDK package checks. The matching Ludus host is authoritative."""
import json
import re
import stat
import sys
import zipfile
from pathlib import Path

PROTOCOL_VERSION = 1  # ludus-plugin-sdk/protocol.ProtocolVersion, not legacy RPC v2.
METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE"}
HOOKS = {"select", "start", "stop", "status", "beforeDelete", "address"}


def validate_manifest(manifest):
    allowed = {"schemaVersion", "protocolVersion", "id", "name", "version", "description", "author", "level", "runAs",
               "executable", "ui", "rangeScoped", "forwardCallerCredentials", "app", "vmActions", "routes", "vmHooks"}
    if not isinstance(manifest, dict) or set(manifest) - allowed:
        raise ValueError("invalid manifest object or unknown field")
    if type(manifest.get("schemaVersion")) is not int or manifest["schemaVersion"] != 1:
        raise ValueError("invalid schema version")
    if not isinstance(manifest.get("id"), str) or not re.fullmatch(r"[a-z][a-z0-9-]{1,63}", manifest["id"]):
        raise ValueError("invalid plugin ID")
    for field, maximum in (("name", 120), ("version", 64), ("description", 2000), ("author", 200)):
        value = manifest.get(field, "")
        if not isinstance(value, str) or len(value.encode("utf-8")) > maximum or (field in ("name", "version") and not value.strip()):
            raise ValueError(f"invalid {field}")
    for field in ("rangeScoped", "forwardCallerCredentials", "app", "vmActions"):
        if field in manifest and type(manifest[field]) is not bool:
            raise ValueError(f"invalid {field}")
    for field in ("level", "runAs", "executable", "ui"):
        if field in manifest and not isinstance(manifest[field], str):
            raise ValueError(f"invalid {field}")
    level, run_as = manifest.get("level") or "standard", manifest.get("runAs") or "ludus"
    if level not in ("standard", "system") or run_as not in ("ludus", "root"):
        raise ValueError("invalid level or runAs")
    executable, ui = manifest.get("executable", ""), manifest.get("ui", "")
    if executable not in ("", "plugin") or ui not in ("", "ui/index.html") or not (executable or ui):
        raise ValueError("invalid executable or UI declaration")
    if "protocolVersion" in manifest and type(manifest["protocolVersion"]) is not int:
        raise ValueError("invalid protocolVersion")
    if executable and manifest.get("protocolVersion") != PROTOCOL_VERSION:
        raise ValueError(f"executable requires SDK protocolVersion {PROTOCOL_VERSION}")
    routes, hooks = manifest.get("routes", []), manifest.get("vmHooks", {})
    if not isinstance(routes, list) or not isinstance(hooks, dict) or set(hooks) - HOOKS or any(type(v) is not bool for v in hooks.values()):
        raise ValueError("invalid routes or vmHooks")
    if not executable and (routes or any(hooks.values()) or manifest.get("forwardCallerCredentials") or manifest.get("app")):
        raise ValueError("routes, hooks, apps and forwarded credentials require an executable")
    if any(hooks.values()) and (level != "system" or run_as != "root"):
        raise ValueError("VM hooks require level system and runAs root")
    if manifest.get("vmActions") and not (manifest.get("app") and all(
            any(isinstance(r, dict) and r.get("method") == method and r.get("pattern") == pattern
                and (r.get("scope") or "plugin") == "plugin" for r in routes)
            for method, pattern in (("GET", "/vm-actions"), ("POST", "/vm-actions/launch")))):
        raise ValueError("vmActions requires app and the SDK's /vm-actions routes")
    names, paths = set(), set()
    for route in routes:
        if not isinstance(route, dict) or set(route) - {"name", "method", "pattern", "scope"}:
            raise ValueError("invalid route object")
        name, method, pattern, scope = (route.get(k, "") for k in ("name", "method", "pattern", "scope"))
        if any(not isinstance(v, str) for v in (name, method, pattern, scope)):
            raise ValueError("invalid route field type")
        scope = scope or "plugin"
        if not name or name in names or method not in METHODS:
            raise ValueError("routes need unique names and supported methods")
        if not pattern.startswith("/") or any(c in pattern for c in "?#{}\\ ") or ".." in pattern or "//" in pattern:
            raise ValueError("route must be an exact absolute path")
        if scope not in ("plugin", "api") or (scope == "api" and (level != "system" or pattern == "/plugins" or pattern.startswith("/plugins/"))):
            raise ValueError("invalid route scope or reserved API path")
        key = (scope, method, pattern)
        if key in paths:
            raise ValueError("duplicate route method/path")
        names.add(name)
        paths.add(key)


def validate(path: Path) -> dict:
    if path.stat().st_size > 128 << 20:
        raise ValueError("ZIP exceeds 128 MiB")
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)) or not set(names) <= {"plugin.json", "plugin", "ui/index.html"}:
            raise ValueError("ZIP has duplicate or unsupported entries")
        if "plugin.json" not in names:
            raise ValueError("missing plugin.json")
        limits = {"plugin.json": 65536, "plugin": 128 << 20, "ui/index.html": 2 << 20}
        for info in archive.infolist():
            mode = stat.S_IFMT(info.external_attr >> 16)
            if info.is_dir() or info.file_size > limits[info.filename] or mode not in (0, stat.S_IFREG):
                raise ValueError(f"invalid or oversized entry: {info.filename}")
        manifest = json.loads(archive.read("plugin.json"))
        validate_manifest(manifest)
        for field, filename in (("executable", "plugin"), ("ui", "ui/index.html")):
            declared = bool(manifest.get(field))
            if declared != (filename in names):
                raise ValueError("declared and packaged entries differ")
            if declared and archive.getinfo(filename).file_size == 0:
                raise ValueError(f"empty {filename}")
    return manifest


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: validate-package.py PACKAGE.zip")
    try:
        validate(Path(sys.argv[1]))
        print(f"valid package: {sys.argv[1]}")
    except (OSError, ValueError, zipfile.BadZipFile) as exc:
        raise SystemExit(f"invalid package: {exc}")
