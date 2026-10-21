#!/usr/bin/env bash
# Pull the latest sources from GitHub, build the Linux plugin and roll it into a
# running CPA deployment.
#
#   REPO_URL=https://github.com/<owner>/<repo>.git ./scripts/deploy_linux.sh
#
# Environment overrides:
#   SRC_DIR      checkout directory            (default ~/anyrouter-cpa-src)
#   DEPLOY_DIR   deployment directory with the  (default ~/anyrouter-cpa)
#                running config.yaml + plugins/ + auths/
#   CONTAINER    container name to restart      (default anyrouter-cpa)
#   BRANCH       branch to track                (default main)
#   SKIP_TESTS   set to 1 to skip go test
#
# The build needs a C toolchain (gcc) and Go; the plugin is a cgo dynamic
# library, so CGO_ENABLED=1 is required.
set -euo pipefail

REPO_URL="${REPO_URL:?set REPO_URL, e.g. REPO_URL=https://github.com/owner/repo.git}"
SRC_DIR="${SRC_DIR:-$HOME/anyrouter-cpa-src}"
DEPLOY_DIR="${DEPLOY_DIR:-$HOME/anyrouter-cpa}"
CONTAINER="${CONTAINER:-anyrouter-cpa}"
BRANCH="${BRANCH:-main}"
SKIP_TESTS="${SKIP_TESTS:-0}"

if ! command -v go >/dev/null 2>&1; then
  export PATH="/usr/local/go/bin:$PATH"
fi
command -v go >/dev/null 2>&1 || { echo "go not found (install Go or set PATH)" >&2; exit 1; }
command -v gcc >/dev/null 2>&1 || { echo "gcc not found (cgo build needs a C toolchain)" >&2; exit 1; }

echo "== fetch =="
if [ -d "$SRC_DIR/.git" ]; then
  git -C "$SRC_DIR" fetch --all --prune
  git -C "$SRC_DIR" checkout "$BRANCH"
  git -C "$SRC_DIR" pull --ff-only
else
  git clone --branch "$BRANCH" "$REPO_URL" "$SRC_DIR"
fi
echo "   commit: $(git -C "$SRC_DIR" log -1 --format='%h %s')"

echo "== test =="
if [ "$SKIP_TESTS" = "1" ]; then
  echo "   skipped"
else
  ( cd "$SRC_DIR" && CGO_ENABLED=0 go test -count=1 ./... )
fi

echo "== build =="
( cd "$SRC_DIR" && mkdir -p dist && CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags "-s -w" -o dist/anyrouter.so . )
python3 - "$SRC_DIR/dist/anyrouter.so" <<'PY'
import ctypes, sys
ctypes.CDLL(sys.argv[1])
print("   dlopen: OK")
PY

echo "== install =="
install -m 0644 "$SRC_DIR/dist/anyrouter.so" "$DEPLOY_DIR/plugins/anyrouter.so"
ls -l "$DEPLOY_DIR/plugins/anyrouter.so"

echo "== restart =="
docker restart "$CONTAINER" >/dev/null
sleep 8
docker logs --since 30s "$CONTAINER" 2>&1 | grep -E "plugin loaded|plugin registered" | tail -2 || true
echo "== done =="
