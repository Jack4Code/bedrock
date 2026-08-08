#!/usr/bin/env bash
# Checks that github.com/Jack4Code/bedrock/grpc can be installed by somebody who
# is not standing inside this repository.
#
# The grpc module builds locally through a `replace` directive pointing at the
# parent in the working tree. A replace is only honoured in the main module, so
# it does nothing for a consumer: they resolve the `require` line instead. That
# makes it possible for grpc/go.mod to be perfectly buildable in CI and
# completely uninstallable in the wild, which is what this guards against.
#
# Exits non-zero when the require is a placeholder or malformed. A require
# naming a real-looking version that is not published yet is reported and
# allowed, because that is the normal state between landing a change and cutting
# the tag — see RELEASING.md.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
PARENT=github.com/Jack4Code/bedrock
SUBMODULE=$PARENT/grpc

cd "$REPO_ROOT"

version=$(awk -v mod="$PARENT" '
  $1 == mod && $2 ~ /^v/ { print $2; exit }
' grpc/go.mod)

if [[ -z $version ]]; then
  echo "FAIL: grpc/go.mod has no require for $PARENT" >&2
  exit 1
fi

echo "grpc/go.mod requires $PARENT $version"

# The pseudo-version go writes when a dependency is resolved entirely through a
# replace. It builds in-tree and can never be fetched.
if [[ $version == v0.0.0-00010101000000-000000000000 ]]; then
  cat >&2 <<EOF
FAIL: the require is the placeholder pseudo-version.

  A replace directive is ignored outside the main module, so every consumer of
  $SUBMODULE resolves this line and gets:

      invalid version: unknown revision 000000000000

  Point the require at a real published $PARENT version. See RELEASING.md.
EOF
  exit 1
fi

if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "FAIL: $version is not a release version consumers can resolve" >&2
  exit 1
fi

# Ask the proxy directly, with the local replace out of the picture.
if ! GOFLAGS= GONOSUMDB= go list -m "$PARENT@$version" >/dev/null 2>&1; then
  cat <<EOF
NOTE: $PARENT@$version is not published yet.

  Expected while a change is landed but not tagged. Tag the parent before
  tagging $SUBMODULE, then re-run this check. See RELEASING.md.
EOF
  exit 0
fi

# The version exists, so a consumer build must work. Do it for real, from a
# throwaway module outside the repo, where the replace cannot help.
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

cat > "$workdir/go.mod" <<EOF
module consumercheck

go 1.25.5
EOF

cat > "$workdir/main.go" <<EOF
package main

import (
	"github.com/Jack4Code/bedrock"
	bgrpc "github.com/Jack4Code/bedrock/grpc"
)

var _ bedrock.Server = (*bgrpc.Server)(nil)

func main() {}
EOF

echo "building a throwaway consumer against $SUBMODULE..."
if ! (cd "$workdir" && go mod tidy >/dev/null 2>&1 && go build ./... >/dev/null 2>&1); then
  echo "FAIL: a module importing $SUBMODULE does not build. Output:" >&2
  (cd "$workdir" && go mod tidy && go build ./...) >&2 || true
  exit 1
fi

echo "OK: $SUBMODULE is consumable."
