#!/bin/sh
# End-to-end check of what a user's `terraform init` does with a release, without publishing anything:
#   1. provider registry protocol (what registry.terraform.io serves): terraform downloads the zip, checks it against
#      SHA256SUMS and checks the GPG signature of SHA256SUMS with the key the registry announces;
#   2. our network mirror (dl.h3llo.cloud/terraform/): terraform checks the zip against the h1:/zh: hashes.
# A local HTTPS server with a throwaway CA plays both hosts. Needs terraform (or tofu), python3, openssl, gpg.
#   scripts/registry-flow-test.sh DIST VERSION KEYS_DIR [terraform|tofu]
# DIST holds the goreleaser output (zips, _SHA256SUMS, _SHA256SUMS.sig) of VERSION; KEYS_DIR/release/*.asc the
# release key(s). Runs as root in a throwaway container (binds :443, edits /etc/hosts).
set -eu
DIST=$(cd "${1:?usage: registry-flow-test.sh DIST VERSION KEYS_DIR [terraform|tofu]}" && pwd)
V=${2:?version}
V=${V#v}
KEYS=$(cd "${3:?keys dir}" && pwd)
TF=${4:-terraform}
HOST=registry.h3test.internal
W=$(mktemp -d)
HERE=$(cd "$(dirname "$0")" && pwd)
trap 'kill $(cat "$W/srv.pid" 2>/dev/null) 2>/dev/null || true; rm -rf "$W"' EXIT
P=terraform-provider-h3
sums=$(ls "$DIST"/${P}_*_SHA256SUMS | head -1)
[ -f "$sums.sig" ] || { echo "no $sums.sig"; exit 1; }
base=$(basename "$sums" _SHA256SUMS) # terraform-provider-h3_<file version>

# --- TLS: throwaway CA and server cert
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=h3test CA" -keyout "$W/ca.key" -out "$W/ca.pem" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=$HOST" -keyout "$W/srv.key" -out "$W/srv.csr" 2>/dev/null
printf 'subjectAltName=DNS:%s\n' "$HOST" > "$W/ext"
openssl x509 -req -in "$W/srv.csr" -CA "$W/ca.pem" -CAkey "$W/ca.key" -CAcreateserial -days 1 -extfile "$W/ext" -out "$W/srv.pem" 2>/dev/null
cat /etc/ssl/certs/ca-certificates.crt "$W/ca.pem" > "$W/bundle.pem" 2>/dev/null || cp "$W/ca.pem" "$W/bundle.pem"
grep -q "$HOST" /etc/hosts || echo "127.0.0.1 $HOST" >> /etc/hosts

# --- static tree: registry protocol + network mirror
R="$W/www"
mkdir -p "$R/.well-known" "$R/dl"
cp "$DIST"/${P}_*.zip "$sums" "$sums.sig" "$R/dl/"
python3 - "$R" "$V" "$base" "$KEYS" "$HOST" <<'PY'
import glob, json, os, re, subprocess, sys
root, v, base, keys, host = sys.argv[1:]
os.makedirs(root + "/v1/providers/h3llo-cloud/h3", exist_ok=True)
json.dump({"providers.v1": "/v1/providers/"}, open(root + "/.well-known/terraform.json", "w"))
gpg_keys = []
for f in sorted(glob.glob(keys + "/release/*.asc")):
    out = subprocess.run(["gpg", "--batch", "--with-colons", "--show-keys", f], capture_output=True, text=True).stdout
    kid = [l.split(":")[4] for l in out.splitlines() if l.startswith("pub:")][0]
    gpg_keys.append({"key_id": kid, "ascii_armor": open(f).read()})
sums = {}
for line in open(glob.glob(root + "/dl/*_SHA256SUMS")[0]):
    h, n = line.split()
    sums[n] = h
plats = []
for n in sorted(sums):
    m = re.match(re.escape(base) + r"_([a-z0-9]+)_([a-z0-9]+)\.zip$", n)
    if not m:
        continue
    os_, arch = m.groups()
    plats.append({"os": os_, "arch": arch})
    d = root + "/v1/providers/h3llo-cloud/h3/%s/download/%s" % (v, os_)
    os.makedirs(d, exist_ok=True)
    json.dump({"protocols": ["6.0"], "os": os_, "arch": arch, "filename": n,
               "download_url": "https://%s/dl/%s" % (host, n), "shasum": sums[n],
               "shasums_url": "https://%s/dl/%s_SHA256SUMS" % (host, base),
               "shasums_signature_url": "https://%s/dl/%s_SHA256SUMS.sig" % (host, base),
               "signing_keys": {"gpg_public_keys": gpg_keys}}, open(d + "/" + arch, "w"))
json.dump({"versions": [{"version": v, "protocols": ["6.0"], "platforms": plats}]},
          open(root + "/v1/providers/h3llo-cloud/h3/versions", "w"))
PY
# network mirror: our real generator (scripts/mirror.py), zips renamed to the requested version
M="$R/terraform/registry.terraform.io/h3llo-cloud/h3"
mkdir -p "$W/mz" "$M"
for z in "$DIST"/${base}_*.zip; do
  n=$(basename "$z"); cp "$z" "$W/mz/${P}_${V}_${n#"${base}"_}"
done
python3 "$HERE/mirror.py" build "$V" "$W/mz" "$M" >/dev/null
cp "$W/mz"/*.zip "$M/"
# the same files under the OpenTofu default host (source "h3llo-cloud/h3" = registry.opentofu.org/... in tofu)
mkdir -p "$R/terraform/registry.opentofu.org/h3llo-cloud"; cp -R "$M" "$R/terraform/registry.opentofu.org/h3llo-cloud/"

cat > "$W/srv.py" <<'PY'
import http.server, os, ssl, sys
os.chdir(sys.argv[1])
class H(http.server.SimpleHTTPRequestHandler):
    def guess_type(self, path):
        return "application/json" if "/v1/" in path or path.endswith(".json") else super().guess_type(path)
    def log_message(self, *a):
        pass
s = http.server.ThreadingHTTPServer(("127.0.0.1", 443), H)
c = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); c.load_cert_chain(sys.argv[2], sys.argv[3])
s.socket = c.wrap_socket(s.socket, server_side=True)
s.serve_forever()
PY
python3 "$W/srv.py" "$R" "$W/srv.pem" "$W/srv.key" & echo $! > "$W/srv.pid"
sleep 1
export SSL_CERT_FILE="$W/bundle.pem" TF_IN_AUTOMATION=1 CHECKPOINT_DISABLE=1

echo "== 1. registry protocol: $TF init verifies SHA256SUMS + GPG signature"
mkdir -p "$W/p1"
printf 'terraform {\n  required_providers {\n    h3 = {\n      source  = "%s/h3llo-cloud/h3"\n      version = "%s"\n    }\n  }\n}\n' "$HOST" "$V" > "$W/p1/main.tf"
(cd "$W/p1" && "$TF" init -input=false -no-color > init.log 2>&1) || { cat "$W/p1/init.log"; exit 1; }
grep -E "Installed" "$W/p1/init.log"
grep -q "key ID" "$W/p1/init.log" || { echo "FAIL: no signature line"; cat "$W/p1/init.log"; exit 1; }

echo "== 1b. a tampered SHA256SUMS must be rejected"
cp "$R/dl/$(basename "$sums")" "$W/sums.bak"; echo "# tampered" >> "$R/dl/$(basename "$sums")"
mkdir -p "$W/p1b"; cp "$W/p1/main.tf" "$W/p1b/"
if (cd "$W/p1b" && "$TF" init -input=false -no-color > init.log 2>&1); then echo "FAIL: tampered checksums accepted"; exit 1; fi
grep -iq "signature\|checksum\|authentication" "$W/p1b/init.log" && echo "   rejected: $(grep -io 'error[^.]*' "$W/p1b/init.log" | head -1)"
cp "$W/sums.bak" "$R/dl/$(basename "$sums")"

echo "== 2. network mirror (dl.h3llo.cloud/terraform/ layout)"
mkdir -p "$W/p2"
cat > "$W/p2/cli.tfrc" <<EOF
provider_installation {
  network_mirror {
    url     = "https://$HOST/terraform/"
    include = ["registry.terraform.io/h3llo-cloud/h3", "registry.opentofu.org/h3llo-cloud/h3"]
  }
}
EOF
printf 'terraform {\n  required_providers {\n    h3 = {\n      source  = "h3llo-cloud/h3"\n      version = "%s"\n    }\n  }\n}\n' "$V" > "$W/p2/main.tf"
(cd "$W/p2" && TF_CLI_CONFIG_FILE="$W/p2/cli.tfrc" TOFU_CLI_CONFIG_FILE="$W/p2/cli.tfrc" "$TF" init -input=false -no-color > init.log 2>&1) ||
  { cat "$W/p2/init.log"; exit 1; }
grep -E "Installed" "$W/p2/init.log"
echo "registry-flow-test: OK ($TF, $V)"
