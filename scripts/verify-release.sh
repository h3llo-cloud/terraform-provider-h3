#!/bin/sh
# Verify a release checksum file against the h3llo.cloud key chain (no secrets, any machine with gpg).
#   scripts/verify-release.sh <SHA256SUMS> <SHA256SUMS.sig> [keys dir, default keys/]
# Accepts the signature only if it is good, made by a key whose primary carries a valid certification by the owner key
# (pinned below) that the owner has not revoked, and the signing key is not revoked. An EXPIRED release key is accepted
# for a signature made while it was valid (Terraform does the same), so old releases stay verifiable after rotation.
# Prints the release key fingerprint on success. Exit 1 on any doubt.
set -eu
OWNER_FPR=${VERIFY_OWNER_FPR:-98E89F853412C3151E0B14C94F28F12C8BE9F40C}
SUMS=${1:?usage: verify-release.sh SHA256SUMS SHA256SUMS.sig [keys-dir]}
SIG=${2:?signature file}
KEYS=${3:-keys}
H=$(mktemp -d)
trap 'gpgconf --homedir "$H" --kill all >/dev/null 2>&1 || true; rm -rf "$H"' EXIT
g() { gpg --homedir "$H" --batch --no-auto-check-trustdb --trust-model always "$@"; }
die() { echo "verify-release: $*" >&2; exit 1; }

# import exit codes are not trusted (gpg 2.4 returns 2 when it cannot start gpg-agent); the key lists are checked instead
[ -f "$KEYS/owner-$OWNER_FPR.asc" ] || die "no owner key $KEYS/owner-$OWNER_FPR.asc"
g --quiet --import "$KEYS/owner-$OWNER_FPR.asc" 2>/dev/null || true
g --with-colons --list-keys "$OWNER_FPR" 2>/dev/null | grep -q "^fpr:::::::::$OWNER_FPR:" || die "owner key file is not $OWNER_FPR"
n=0
for f in "$KEYS"/release/*.asc; do
	[ -f "$f" ] || continue
	g --quiet --import "$f" 2>/dev/null || true
	n=$((n + 1))
done
[ "$n" -gt 0 ] || die "no release keys in $KEYS/release/"

st=$(g --status-fd 1 --verify "$SIG" "$SUMS" 2>/dev/null || true)
echo "$st" | grep -q '^\[GNUPG:\] REVKEYSIG' && die "signed by a REVOKED key"
echo "$st" | grep -Eq '^\[GNUPG:\] (GOODSIG|EXPKEYSIG) ' || die "signature of $SUMS is NOT valid"
# VALIDSIG <fpr> <date> <ts> <expire> <ver> <reserved> <pkalgo> <hash> <class> <primary fpr>
primary=$(echo "$st" | awk '$2=="VALIDSIG"{print $12; exit}')
[ -n "$primary" ] || die "no VALIDSIG"
oid=$(echo "$OWNER_FPR" | cut -c25-40)
cs=$(g --with-colons --check-sigs "$primary" 2>/dev/null)
echo "$cs" | awk -F: -v o="$oid" '$1=="sig" && $2=="!" && toupper($5)==o && $11 ~ /^1[0-3]x$/ {ok=1} END{exit !ok}' ||
	die "key $primary is not certified by the owner $OWNER_FPR"
echo "$cs" | awk -F: -v o="$oid" '$1=="rev" && $2=="!" && toupper($5)==o && $11 ~ /^30x$/ {r=1} END{exit !r}' &&
	die "the owner revoked the certification of $primary"
echo "$cs" | awk -F: '$1=="pub" && $2=="r" {r=1} END{exit !r}' && die "key $primary is revoked"
echo "$primary"
