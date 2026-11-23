#!/bin/sh
# Publish the source of tag $1 to github.com/$GITHUB_REPO as ONE commit on top of GitHub main, then push the tag.
# The internal GitLab history is not published (same as the manual squash releases 0.1.1 and 0.1.2). The commit is
# deterministic (tree, parent and dates of the tag), so a re-run pushes the same SHA. A tag that already exists on
# GitHub is never moved: same tree = ok, other tree = error.
# Internal files (CI of GitLab, gitleaks baseline) are left out of the public tree, as in 0.1.1 and 0.1.2:
# PRIVATE_PATHS below; the published tree is the tag tree without them.
#   GITHUB_TOKEN=... scripts/github-sync.sh v0.3.0
set -eu
TAG=${1:?usage: github-sync.sh vX.Y.Z}
REPO=${GITHUB_REPO:-h3llo-cloud/terraform-provider-h3}
: "${GITHUB_TOKEN:?GITHUB_TOKEN is not set}"
URL=${GITHUB_URL:-https://github.com/$REPO.git} # GITHUB_URL: tests only
# The token reaches git only through the credential helper (never in the URL or argv, never printed).
HELPER='!f(){ echo username=x-access-token; echo "password=$GITHUB_TOKEN"; }; f'
g() { git -c credential.helper= -c "credential.helper=$HELPER" "$@"; }

PRIVATE_PATHS=${PRIVATE_PATHS:-.gitlab-ci.yml .gitlab .gitleaks-baseline.json}
idx=$(mktemp); trap 'rm -f "$idx"' EXIT
GIT_INDEX_FILE=$idx git read-tree "$TAG^{tree}"
# shellcheck disable=SC2086 # word splitting of the path list is intended
GIT_INDEX_FILE=$idx git rm -r -q --cached --ignore-unmatch -- $PRIVATE_PATHS
tree=$(GIT_INDEX_FILE=$idx git write-tree)
g fetch --quiet --no-tags "$URL" "+refs/heads/main:refs/remotes/github/main"
if g fetch --quiet --no-tags "$URL" "+refs/tags/$TAG:refs/remotes/github/tag" 2>/dev/null; then
	if [ "$(git rev-parse refs/remotes/github/tag^{tree})" = "$tree" ]; then
		echo "github-sync: $TAG already on GitHub with the same tree"
		exit 0
	fi
	echo "github-sync: $TAG exists on GitHub with ANOTHER tree; public tags are never moved" >&2
	exit 1
fi

parent=$(git rev-parse refs/remotes/github/main)
if [ "$(git rev-parse "$parent^{tree}")" = "$tree" ]; then
	commit=$parent
else
	date=$(git log -1 --format=%cI "$TAG")
	commit=$(GIT_AUTHOR_NAME="h3llo cloud" GIT_AUTHOR_EMAIL="support@h3llo.cloud" GIT_AUTHOR_DATE="$date" \
		GIT_COMMITTER_NAME="h3llo cloud" GIT_COMMITTER_EMAIL="support@h3llo.cloud" GIT_COMMITTER_DATE="$date" \
		git commit-tree "$tree" -p "$parent" -m "Release $TAG")
fi
g push --quiet "$URL" "$commit:refs/heads/main" "$commit:refs/tags/$TAG"
echo "github-sync: $REPO main and $TAG -> $commit"
