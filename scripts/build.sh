#!/bin/sh
set -eu

usage() {
  echo "usage: [PLUGIN_VERSION=X.Y.Z] [GOARCH=amd64|arm64] $0 LUDUS_PLUGIN_SDK_DIR OUTPUT_DIRECTORY" >&2
  echo "       [PLUGIN_VERSION=X.Y.Z] $0 OUTPUT_DIRECTORY   (UI-only plugin with source/plugin.json)" >&2
  exit 2
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
plugin_root="$repo_root/source"
# CI supplies the release tag; local builds use the checkout's tag/description.
version=${PLUGIN_VERSION:-$(git -C "$repo_root" describe --tags --dirty 2>/dev/null || true)}
if [ -z "$version" ]; then
  echo "Set PLUGIN_VERSION or build from a git checkout with a version tag" >&2
  exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ludus-plugin-build.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

# A hand-written manifest is only for UI-only plugins. Executable plugins
# always derive plugin.json from the SDK declaration in source/main.go.
if [ -f "$plugin_root/plugin.json" ]; then
  [ "$#" -eq 1 ] || usage
  mkdir -p "$1"
  output_dir=$(CDPATH= cd -- "$1" && pwd)
  python3 - "$plugin_root/plugin.json" "$version" "$tmp/plugin.json" <<'PY'
import json, sys
manifest = json.load(open(sys.argv[1], encoding="utf-8"))
if manifest.get("executable"):
    raise SystemExit("Remove source/plugin.json for SDK builds; the executable generates it")
manifest["version"] = sys.argv[2]
open(sys.argv[3], "w", encoding="utf-8").write(json.dumps(manifest, indent=2) + "\n")
PY
else
  [ "$#" -eq 2 ] || usage
  mkdir -p "$2"
  output_dir=$(CDPATH= cd -- "$2" && pwd)
  sdk_root=$(CDPATH= cd -- "$1" && pwd)
  if [ -d "$sdk_root/ludus-plugin-sdk" ]; then
    sdk_root="$sdk_root/ludus-plugin-sdk"
  fi
  if ! grep -qx 'module gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk' "$sdk_root/go.mod" 2>/dev/null; then
    echo "$sdk_root is not the Ludus plugin SDK (ludus-plugin-sdk)" >&2
    exit 1
  fi
  ldflags="-X main.version=$version"
  # Only these two modules are loaded; no Ludus server, client or database code.
  cat >"$tmp/go.work" <<EOF
go 1.25.0

use (
	$plugin_root
	$sdk_root
)
EOF
  export GOWORK="$tmp/go.work" GOFLAGS=-mod=readonly
  target_arch=${GOARCH:-amd64}
  export GOOS="$(go env GOHOSTOS)" GOARCH="$(go env GOHOSTARCH)"
  cd "$plugin_root"
  go vet ./...
  CGO_ENABLED=1 go test -race ./...
  # plugin.json comes from the plugin's own declaration, so it cannot drift.
  # Generate it with a host-native build; the Linux binary may not run here.
  go build -buildvcs=false -ldflags "$ldflags" -o "$tmp/manifest-tool" .
  "$tmp/manifest-tool" --ludus-manifest >"$tmp/plugin.json"
  CGO_ENABLED=0 GOOS=linux GOARCH="$target_arch" \
    go build -buildvcs=false -trimpath -ldflags "-s -w $ldflags" -o "$tmp/plugin" .
fi

python3 -m unittest discover -s "$repo_root/scripts" -p 'test_*.py'
package_name=$(python3 - "$plugin_root" "$tmp" <<'PY'
import json, re, sys, zipfile
from pathlib import Path
source, tmp = map(Path, sys.argv[1:])
manifest = json.loads((tmp / "plugin.json").read_text())
# Always create a fresh archive so removed entries cannot survive a rebuild.
with zipfile.ZipFile(tmp / "package.zip", "w", zipfile.ZIP_DEFLATED) as archive:
    archive.write(tmp / "plugin.json", "plugin.json")
    if manifest.get("executable"):
        archive.write(tmp / "plugin", "plugin")
    if manifest.get("ui"):
        archive.write(source / "ui/index.html", "ui/index.html")
version = re.sub(r"[^A-Za-z0-9._-]", "_", manifest["version"])
print(f"{manifest['id']}-{version}.zip")
PY
)
python3 "$repo_root/scripts/validate-package.py" "$tmp/package.zip"
output="$output_dir/$package_name"
mv "$tmp/package.zip" "$output"
python3 - "$output" <<'PY'
import hashlib, sys
from pathlib import Path
path = Path(sys.argv[1])
path.with_name(path.name + '.sha256').write_text(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + path.name + '\n')
PY
echo "$output"
