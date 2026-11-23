#!/bin/sh
# Post-release smoke: what a user gets after the tag pipeline published vX.Y.Z. No secrets.
#   1. GitHub release: SHA256SUMS signed by a release key certified by the owner (scripts/verify-release.sh), the zip of
#      this platform matches SHA256SUMS;
#   2. network mirror on $DL_PUBLIC_BASE/terraform/ (if it lists the version): `terraform init` of exactly this version;
#   3. Terraform Registry: waits up to ~10 min for the webhook ingestion, then `terraform init` from the registry.
# Exit 3 (job shows a warning) when only step 3 cannot be completed from the runner.
#   scripts/release-smoke.sh v0.3.0
set -eu
TAG=${1:?usage: release-smoke.sh vX.Y.Z}
V=${TAG#v}
REPO=${GITHUB_REPO:-h3llo-cloud/terraform-provider-h3}
DL=${DL_PUBLIC_BASE:-https://dl.h3llo.cloud}
P=terraform-provider-h3
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
get() { curl -fsSL --retry 5 --retry-delay 5 -o "$2" "$1"; }
case "$(uname -m)" in x86_64 | amd64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; *) echo "unsupported arch"; exit 1 ;; esac

echo "== 1. GitHub release $REPO $TAG"
base="https://github.com/$REPO/releases/download/$TAG"
get "$base/${P}_${V}_SHA256SUMS" "$W/SUMS"
get "$base/${P}_${V}_SHA256SUMS.sig" "$W/SUMS.sig"
k=$(sh "$HERE/verify-release.sh" "$W/SUMS" "$W/SUMS.sig" "$HERE/../keys")
echo "   signature OK, release key $k"
z="${P}_${V}_linux_${arch}.zip"
get "$base/$z" "$W/$z"
want=$(awk -v f="$z" '$2==f{print $1}' "$W/SUMS")
got=$(sha256sum "$W/$z" | awk '{print $1}')
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "SHA-256 of $z does not match SHA256SUMS"; exit 1; }
echo "   $z matches SHA256SUMS"

tf_init() { # dir
	printf 'terraform {\n  required_providers {\n    h3 = {\n      source  = "h3llo-cloud/h3"\n      version = "%s"\n    }\n  }\n}\n' "$V" > "$1/main.tf"
	(cd "$1" && terraform init -input=false -no-color > init.log 2>&1) || { cat "$1/init.log"; return 1; }
	grep -E "Installed" "$1/init.log"
}

echo "== 2. network mirror $DL/terraform/"
if curl -fsSL "$DL/terraform/registry.terraform.io/h3llo-cloud/h3/index.json" 2>/dev/null | grep -q "\"$V\""; then
	mkdir -p "$W/m"
	printf 'provider_installation {\n  network_mirror {\n    url     = "%s/terraform/"\n    include = ["registry.terraform.io/h3llo-cloud/h3"]\n  }\n}\n' "$DL" > "$W/m/cli.tfrc"
	TF_CLI_CONFIG_FILE="$W/m/cli.tfrc" tf_init "$W/m"
else
	echo "   the mirror does not list $V (DL_S3_* not configured?) — skipped"
fi

echo "== 3. Terraform Registry"
i=0
until curl -fsS --max-time 20 "https://registry.terraform.io/v1/providers/h3llo-cloud/h3/versions" 2>/dev/null | grep -q "\"$V\""; do
	i=$((i + 1))
	if [ "$i" -ge 20 ]; then
		echo "   registry.terraform.io does not list $V after ~10 min (or is unreachable from the runner)"
		exit 3
	fi
	sleep 30
done
mkdir -p "$W/r"
tf_init "$W/r" || exit 3
echo "release-smoke: OK $TAG"
