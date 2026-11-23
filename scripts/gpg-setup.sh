#!/bin/sh
# Import the CI release signing key and check that it chains to the owner's key before anything is signed.
# The same script lives in h3terraform and h3cli. Key model and ceremony: keys/README.md.
#
# Chain: owner key 98E8 9F85 3412 C315 1E0B 14C9 4F28 F12C 8BE9 F40C (YubiKey, offline, never in CI)
#        --certifies--> release key (RSA-4096, 1 year, public part committed in keys/release/) --signs--> SHA256SUMS.
#
# Source of the private key (first that is set):
#   1. OpenBao KV (later): OPENBAO_ADDR + OPENBAO_ID_TOKEN (GitLab id_token, aud=openbao), role OPENBAO_ROLE (default
#      $CI_PROJECT_NAME-release, bound to this project, ref_type=tag, ref_protected=true), secret OPENBAO_GPG_PATH
#      (default ci/data/release/gpg) with fields private_key, passphrase, fingerprint;
#   2. protected CI variables (now): GPG_PRIVATE_KEY_FILE (type File), GPG_PASSPHRASE (masked), GPG_FINGERPRINT.
# Refuses to sign unless the key
#   - is RSA >= 4096 (Terraform Registry: RSA/DSA only), signing-capable, not revoked, valid for 7 more days;
#   - is committed in keys/release/*.asc (a new key needs a reviewed MR, a CI variable alone is not enough);
#   - carries a valid, not revoked certification by the owner key, whose fingerprint is pinned below.
# Writes /tmp/gpg.env with GPG_FINGERPRINT ("<fpr>!") and GPG_PASSPHRASE for goreleaser. Never prints secrets.
set -eu

OWNER_FPR=98E89F853412C3151E0B14C94F28F12C8BE9F40C
KEYS_DIR=keys
if [ "${H3_SIGNING_SELFTEST:-}" = 1 ]; then
  # MR pipelines and local tests: a throwaway owner key and keys dir. Never on a tag.
  [ -z "${CI_COMMIT_TAG:-}" ] || { echo "gpg-setup: selftest mode is refused on a tag"; exit 1; }
  OWNER_FPR=${SELFTEST_OWNER_FPR:?}
  KEYS_DIR=${SELFTEST_KEYS_DIR:?}
fi
umask 077
mkdir -p "${GNUPGHOME:-$HOME/.gnupg}"
chmod 700 "${GNUPGHOME:-$HOME/.gnupg}"
printf 'no-auto-check-trustdb\ntrust-model always\n' >> "${GNUPGHOME:-$HOME/.gnupg}/gpg.conf"
key=/tmp/release-key.asc

if [ -n "${OPENBAO_ADDR:-}" ]; then
  : "${OPENBAO_ID_TOKEN:?OPENBAO_ID_TOKEN (id_tokens) is not set}"
  token=$(wget -qO- --header 'Content-Type: application/json' \
    --post-data "{\"role\":\"${OPENBAO_ROLE:-${CI_PROJECT_NAME:-h3}-release}\",\"jwt\":\"$OPENBAO_ID_TOKEN\"}" \
    "$OPENBAO_ADDR/v1/auth/jwt/login" | python3 -c 'import json,sys; print(json.load(sys.stdin)["auth"]["client_token"])')
  wget -qO /tmp/gpg-secret.json --header "X-Vault-Token: $token" "$OPENBAO_ADDR/v1/${OPENBAO_GPG_PATH:-ci/data/release/gpg}"
  python3 - /tmp/gpg-secret.json "$key" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))["data"]
d = d.get("data", d)  # KV v2 wraps the fields once more
open(sys.argv[2], "w").write(d["private_key"])
with open("/tmp/gpg.env", "w") as f:
    f.write("GPG_PASSPHRASE='%s'\n" % d["passphrase"].replace("'", "'\\''"))
    f.write("GPG_FINGERPRINT_RAW=%s\n" % d["fingerprint"].replace(" ", ""))
PY
  rm -f /tmp/gpg-secret.json
  . /tmp/gpg.env
  fpr=$GPG_FINGERPRINT_RAW
else
  for v in GPG_PRIVATE_KEY_FILE GPG_PASSPHRASE GPG_FINGERPRINT; do
    eval "[ -n \"\${$v:-}\" ]" || { echo "gpg-setup: $v is not set (and no OPENBAO_ADDR)"; exit 1; }
  done
  cp "$GPG_PRIVATE_KEY_FILE" "$key"
  fpr=$(echo "$GPG_FINGERPRINT" | tr -d ' !')
  printf "GPG_PASSPHRASE='%s'\n" "$(printf %s "$GPG_PASSPHRASE" | sed "s/'/'\\\\''/g")" > /tmp/gpg.env
fi

owner_file="$KEYS_DIR/owner-$OWNER_FPR.asc"
[ -f "$owner_file" ] || { echo "gpg-setup: no $owner_file"; exit 1; }
gpg --batch --quiet --import "$owner_file" 2>/dev/null
for f in "$KEYS_DIR"/release/*.asc; do
  [ -f "$f" ] && gpg --batch --quiet --import "$f" 2>/dev/null
done
gpg --batch --quiet --import "$key" 2>/dev/null
rm -f "$key"

# Fingerprints of the committed release keys (primary keys only).
for f in "$KEYS_DIR"/release/*.asc; do
  [ -f "$f" ] && gpg --batch --with-colons --show-keys "$f" 2>/dev/null | awk -F: '$1=="pub"{p=1;next} p&&$1=="fpr"{print $10;p=0}'
done > /tmp/committed-fprs.txt
gpg --batch --with-colons --fixed-list-mode --list-secret-keys > /tmp/gpg-keys.txt
gpg --batch --with-colons --fixed-list-mode --list-keys "$OWNER_FPR" > /tmp/gpg-owner.txt 2>/dev/null || true
python3 - "$fpr" "$OWNER_FPR" /tmp/gpg-keys.txt /tmp/committed-fprs.txt /tmp/gpg-owner.txt <<'PY'
import subprocess, sys, time
want, owner, keys, committed, owner_list = sys.argv[1].upper(), sys.argv[2].upper(), sys.argv[3], sys.argv[4], sys.argv[5]

if not any(l.startswith("fpr:") and l.split(":")[9].upper() == owner for l in open(owner_list)):
    sys.exit("gpg-setup: the owner key file does not contain %s" % owner)

# sec/ssb: 2 validity, 3 length, 4 algo, 7 expiry, 12 capabilities; the fpr line follows its key.
found, primary, last = None, None, None
for line in open(keys):
    f = line.rstrip("\n").split(":")
    if f[0] in ("sec", "ssb"):
        last = f
    elif f[0] == "fpr" and last is not None:
        if last[0] == "sec":
            primary = f[9].upper()
        if f[9].upper() == want:
            found = (last, primary)
        last = None
if not found:
    sys.exit("gpg-setup: GPG_FINGERPRINT %s is not among the imported secret keys" % want)
k, prim = found
if k[1] in ("r", "e", "i", "d", "n"):
    sys.exit("gpg-setup: the signing key is revoked, expired or invalid (validity %r)" % k[1])
if k[6] and int(k[6]) < time.time() + 7 * 86400:
    sys.exit("gpg-setup: the signing key expires within 7 days: rotate it (keys/README.md)")
if "s" not in k[11].lower():
    sys.exit("gpg-setup: the key is not signing-capable (capabilities %r)" % k[11])
if k[3] != "1" or int(k[2] or 0) < 4096:
    sys.exit("gpg-setup: the key must be RSA >= 4096 for the Terraform Registry (algo %s, %s bits)" % (k[3], k[2]))
if prim not in {l.strip().upper() for l in open(committed) if l.strip()}:
    sys.exit("gpg-setup: key %s is not committed in keys/release/ (add its public key with the owner's certification by MR)" % prim)

# The owner's certification: a good (!) sig of class 10x-13x issued by the owner key, not revoked by the owner.
out = subprocess.run(["gpg", "--batch", "--with-colons", "--check-sigs", prim], capture_output=True, text=True).stdout
oid = owner[-16:]
cert = [l.split(":") for l in out.splitlines() if l.startswith("sig:")]
revs = [l.split(":") for l in out.splitlines() if l.startswith("rev:")]
if not any(s[1] == "!" and s[4].upper() == oid and s[10][:2] in ("10", "11", "12", "13") for s in cert):
    sys.exit("gpg-setup: key %s has no valid certification by the owner key %s" % (prim, owner))
if any(s[1] == "!" and s[4].upper() == oid and s[10].startswith("30") for s in revs):
    sys.exit("gpg-setup: the owner revoked the certification of %s" % prim)
exp = time.strftime("%Y-%m-%d", time.gmtime(int(k[6]))) if k[6] else "never"
print("gpg-setup: release key OK: %s (certified by owner %s), expires %s" % (want, owner[-16:], exp))
PY
rm -f /tmp/gpg-keys.txt /tmp/committed-fprs.txt /tmp/gpg-owner.txt
echo "GPG_FINGERPRINT=$fpr!" >> /tmp/gpg.env
