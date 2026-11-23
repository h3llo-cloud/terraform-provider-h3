#!/bin/sh
# THROWAWAY key chain for MR pipelines and local tests of the release signing (never used for a real release):
# a fake "owner" ed25519 key certifies a fresh RSA-4096 release key, exactly like the owner's ceremony does.
#   scripts/selftest-keys.sh /tmp/selftest
# Output: DIR/keys/owner-<fpr>.asc, DIR/keys/release/selftest.asc (public, with the certification),
#         DIR/ci/GPG_PRIVATE_KEY_FILE.asc + GPG_PASSPHRASE.txt + GPG_FINGERPRINT.txt, DIR/env (SELFTEST_* for gpg-setup.sh).
set -eu
D=${1:?usage: selftest-keys.sh DIR}
umask 077
rm -rf "$D"
mkdir -p "$D/keys/release" "$D/ci" "$D/g"
G="$D/g"
echo allow-loopback-pinentry > "$G/gpg-agent.conf"
printf 'no-auto-check-trustdb\ntrust-model always\n' > "$G/gpg.conf"
g() { gpg --homedir "$G" --batch --quiet --pinentry-mode loopback "$@"; }
g --passphrase '' --quick-generate-key 'SELFTEST owner (throwaway) <selftest@invalid>' ed25519 cert,sign 30d
OF=$(gpg --homedir "$G" --with-colons -k 'SELFTEST owner' | awk -F: '$1=="fpr"{print $10;exit}')
head -c 30 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$D/ci/GPG_PASSPHRASE.txt"
g --passphrase-file "$D/ci/GPG_PASSPHRASE.txt" --quick-generate-key 'SELFTEST release CI (throwaway) <selftest@invalid>' rsa4096 sign 30d
RF=$(gpg --homedir "$G" --with-colons -k 'SELFTEST release' | awk -F: '$1=="fpr"{print $10;exit}')
g --passphrase "" -u "$OF!" --quick-sign-key "$RF" >/dev/null
gpg --homedir "$G" --armor --export "$OF" > "$D/keys/owner-$OF.asc"
gpg --homedir "$G" --armor --export "$RF" > "$D/keys/release/selftest.asc"
g --passphrase-file "$D/ci/GPG_PASSPHRASE.txt" --armor --export-secret-keys "$RF" > "$D/ci/GPG_PRIVATE_KEY_FILE.asc"
echo "$RF" > "$D/ci/GPG_FINGERPRINT.txt"
gpgconf --homedir "$G" --kill all 2>/dev/null || true
rm -rf "$G"
cat > "$D/env" <<ENV
H3_SIGNING_SELFTEST=1
SELFTEST_OWNER_FPR=$OF
SELFTEST_KEYS_DIR=$D/keys
VERIFY_OWNER_FPR=$OF
GPG_PRIVATE_KEY_FILE=$D/ci/GPG_PRIVATE_KEY_FILE.asc
GPG_FINGERPRINT=$RF
ENV
echo "selftest keys: owner $OF -> release $RF (throwaway)"
