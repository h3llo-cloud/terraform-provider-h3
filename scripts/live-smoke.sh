#!/bin/sh
# Live smoke: apply -> plan must be empty (no drift against the real API) -> destroy, always destroying on exit.
#   PROVIDER_BIN_DIR=bin H3_API_ENDPOINT=... H3_KEY_ID=... H3_SECRET_KEY=... H3_PROJECT_ID=... scripts/live-smoke.sh [terraform|tofu]
set -eu
TF=${1:-terraform}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN=${PROVIDER_BIN_DIR:-$ROOT/bin}
RUN=${SMOKE_RUN:-${CI_PIPELINE_ID:-local$(date +%s)}}
for v in H3_API_ENDPOINT H3_KEY_ID H3_SECRET_KEY H3_PROJECT_ID; do
	eval "[ -n \"\${$v:-}\" ]" || { echo "live-smoke: $v is not set" >&2; exit 1; }
done
WORK=$(mktemp -d)
cp "$ROOT/scripts/live-smoke/main.tf" "$WORK/"
cat >"$WORK/tfrc" <<RC
provider_installation {
  dev_overrides {
    "h3llo-cloud/h3" = "$BIN"
    "registry.terraform.io/h3llo-cloud/h3" = "$BIN"
    "registry.opentofu.org/h3llo-cloud/h3" = "$BIN"
  }
  direct {}
}
RC
export TF_CLI_CONFIG_FILE="$WORK/tfrc" TF_IN_AUTOMATION=1 TF_VAR_run="$RUN"
cd "$WORK"
cleanup() {
	rc=$?
	"$TF" destroy -auto-approve -no-color -input=false >destroy.log 2>&1 || { echo "live-smoke: DESTROY FAILED, leftovers tfacc-$RUN" >&2; tail -40 destroy.log >&2; rc=1; }
	rm -rf "$WORK"
	exit $rc
}
trap cleanup EXIT INT TERM
"$TF" apply -auto-approve -no-color -input=false
set +e
"$TF" plan -detailed-exitcode -no-color -input=false >plan.log 2>&1
code=$?
set -e
case $code in
	0) echo "live-smoke: plan after apply is empty" ;;
	2) echo "live-smoke: DRIFT after apply:" >&2; cat plan.log >&2; exit 1 ;;
	*) cat plan.log >&2; exit 1 ;;
esac
