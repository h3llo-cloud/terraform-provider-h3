#!/bin/sh
# Copy of a provider release inside GitLab: dist/ files -> generic package terraform-provider-h3/<version>,
# GitLab release of the tag with links to them and the CHANGELOG notes. Uses CI_JOB_TOKEN only.
#   scripts/gitlab-release.sh v0.3.0 /tmp/release-notes.md
set -eu
TAG=${1:?usage: gitlab-release.sh vX.Y.Z notes.md}
NOTES=${2:?notes file}
V=${TAG#v}
PKG="$CI_API_V4_URL/projects/$CI_PROJECT_ID/packages/generic/terraform-provider-h3/$V"
links=""
# goreleaser uploads the manifest from the repo root under the release name; dist/ has no copy of it.
cp terraform-registry-manifest.json "dist/terraform-provider-h3_${V}_manifest.json"
for f in dist/*.zip dist/*_SHA256SUMS dist/*_SHA256SUMS.sig dist/*_manifest.json; do
	[ -f "$f" ] || continue
	n=$(basename "$f")
	curl -fsS -o /dev/null --header "JOB-TOKEN: $CI_JOB_TOKEN" --upload-file "$f" "$PKG/$n"
	links="$links $n"
done
python3 - "$TAG" "$NOTES" "$PKG" $links >/tmp/release.json <<'PY'
import json, sys
tag, notes, pkg, names = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
print(json.dumps({
    "tag_name": tag, "name": "terraform-provider-h3 " + tag,
    "description": open(notes).read(),
    "assets": {"links": [{"name": n, "url": pkg + "/" + n, "link_type": "package"} for n in names]},
}))
PY
code=$(curl -sS -o /tmp/release.out -w '%{http_code}' --header "JOB-TOKEN: $CI_JOB_TOKEN" \
	--header "Content-Type: application/json" --data @/tmp/release.json "$CI_API_V4_URL/projects/$CI_PROJECT_ID/releases")
case "$code" in
	201) echo "gitlab-release: $TAG created" ;;
	409) echo "gitlab-release: $TAG already exists" ;;
	*) echo "gitlab-release: HTTP $code" >&2; head -c 500 /tmp/release.out >&2; exit 1 ;;
esac
