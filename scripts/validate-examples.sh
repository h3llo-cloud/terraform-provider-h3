#!/bin/sh
# Validate every example against the locally built provider (dev_overrides, no registry access).
# usage: scripts/validate-examples.sh [terraform|tofu]
set -eu
TF=${1:-terraform}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN=${PROVIDER_BIN_DIR:-$ROOT/bin}
if [ ! -x "$BIN/terraform-provider-h3" ]; then
  mkdir -p "$BIN"
  (cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN/terraform-provider-h3" .)
fi
RC=$(mktemp)
cat > "$RC" <<RC
provider_installation {
  dev_overrides {
    "h3llo-cloud/h3" = "$BIN"
    "registry.terraform.io/h3llo-cloud/h3" = "$BIN"
    "registry.opentofu.org/h3llo-cloud/h3" = "$BIN"
  }
  direct {}
}
RC
export TF_CLI_CONFIG_FILE="$RC"
rc=0
"$TF" fmt -check -recursive "$ROOT/examples" || { echo "FAIL: $TF fmt -check (run: $TF fmt -recursive examples)"; rc=1; }
# Scenario examples (examples/<name>/main.tf). Registry snippets in
# examples/{provider,resources,data-sources} reference undeclared variables
# and are checked by fmt only.
for d in "$ROOT"/examples/*/; do
  [ -f "$d/main.tf" ] || continue
  if (cd "$d" && "$TF" validate -no-color >/tmp/validate.$$ 2>&1); then
    echo "ok   $(basename "$d")"
  else
    echo "FAIL $(basename "$d")"; cat /tmp/validate.$$; rc=1
  fi
done
rm -f "$RC" /tmp/validate.$$
exit $rc
