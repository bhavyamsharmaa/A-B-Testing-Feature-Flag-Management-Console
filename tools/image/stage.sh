#!/usr/bin/env bash
# Builds, without a container runtime, exactly what the Dockerfile's FINAL stage contains, and checks it.
#
#   tools/image/stage.sh [os] [arch]        # default: linux and this machine's architecture
#
# It runs the same commands as the Dockerfile's build stage (same flags: static, -trimpath, stripped, no build id, time
# zones embedded) and lays the result out the way the final stage does (/app/helios, /app/bin/*, the CA certificates,
# /etc/passwd and /etc/group naming the user 65532). Then tools/imagecheck inspects that tree. This is NOT a container:
# it cannot show the image's real size or that `docker build` works (that needs a runtime; see docs/DEPLOYMENT.md), but
# it does show that every command builds statically for the target, how big the binaries are, and that nothing that
# must not ship would. A host-OS copy of the binaries (for the smoke test) is built too.
#
# Output lives in ${HELIOS_LOCAL_DIR:-$HOME/helios-local}/image-stage (never inside the repository).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd -P)"
BASE="${HELIOS_LOCAL_DIR:-$HOME/helios-local}"
case "$(cd "$(dirname "$BASE")" && pwd -P)/$(basename "$BASE")/" in
  "$REPO"/*) echo "stage.sh: $BASE is inside the repository; refusing" >&2; exit 2 ;;
esac
STAGE="$BASE/image-stage"
TARGET_OS="${1:-linux}"
TARGET_ARCH="${2:-$(go env GOARCH)}"
COMMANDS="api scheduler outbox purgeworkspaces purgeevents segmentusage"

fresh() { # an empty directory, recreated; the path is checked before anything is deleted
  local d="${1:?}"
  case "$d" in "$BASE"/image-stage/*) ;; *) echo "stage.sh: refusing to clear $d" >&2; exit 2 ;; esac
  rm -rf -- "${d:?}"
  mkdir -p "$d"
}

build_all() { # os arch bindir helios
  local os="$1" arch="$2" bindir="$3" helios="$4"
  ( cd "$REPO/backend"
    export CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
    for c in $COMMANDS; do
      GOOS="$os" GOARCH="$arch" go build -tags timetzdata -ldflags="-s -w -buildid=" -o "$bindir/$c" "./cmd/$c"
    done
    GOOS="$os" GOARCH="$arch" go build -tags timetzdata -ldflags="-s -w -buildid=" -o "$helios" ./cmd/helios )
}

# --- the image's final stage, as a directory ---
ROOT="$STAGE/rootfs"
fresh "$ROOT"
mkdir -p "$ROOT/app/bin" "$ROOT/etc/ssl/certs"
build_all "$TARGET_OS" "$TARGET_ARCH" "$ROOT/app/bin" "$ROOT/app/helios"
for ca in /etc/ssl/certs/ca-certificates.crt /etc/ssl/cert.pem; do
  if [ -f "$ca" ]; then cp "$ca" "$ROOT/etc/ssl/certs/ca-certificates.crt"; break; fi
done
printf 'helios:x:65532:65532:helios:/nonexistent:/sbin/nologin\n' > "$ROOT/etc/passwd"
printf 'helios:x:65532:\n' > "$ROOT/etc/group"
chmod 0644 "$ROOT/etc/passwd" "$ROOT/etc/group" "$ROOT/etc/ssl/certs/ca-certificates.crt"

# --- a copy for this machine, to run the smoke test on ---
HOST="$STAGE/host"
fresh "$HOST"
mkdir -p "$HOST/bin"
build_all "$(go env GOOS)" "$(go env GOARCH)" "$HOST/bin" "$HOST/helios"

echo "staged ${TARGET_OS}/${TARGET_ARCH} in $ROOT and the host build in $HOST"
echo "binaries (stripped, static):"
( cd "$ROOT/app" && ls -l helios bin/* | awk '{printf "  %10.1f KiB  %s\n", $5/1024, $9}' )
( cd "$REPO/tools/imagecheck" && go run . -rootfs "$ROOT" -q )
