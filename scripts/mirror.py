#!/usr/bin/env python3
"""Provider network mirror on our own S3 (dl.h3llo.cloud) and a tiny S3 uploader. Stdlib only.

Terraform / OpenTofu users who cannot reach registry.terraform.io or GitHub point the CLI at the mirror:

    provider_installation {
      network_mirror { url = "https://dl.h3llo.cloud/terraform/"  include = ["registry.terraform.io/h3llo-cloud/h3"] }
      direct { exclude = ["registry.terraform.io/h3llo-cloud/h3"] }
    }

Layout (network mirror protocol, https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol):
    terraform/registry.terraform.io/h3llo-cloud/h3/index.json        {"versions": {"0.3.0": {}, ...}}
    terraform/registry.terraform.io/h3llo-cloud/h3/0.3.0.json        {"archives": {"linux_amd64": {"url", "hashes"}}}
    terraform/registry.terraform.io/h3llo-cloud/h3/<zip>, _SHA256SUMS, _SHA256SUMS.sig, _manifest.json

  mirror.py build VERSION DIST OUT [--index URL_OR_FILE]   write OUT/<v>.json and the merged OUT/index.json
  mirror.py upload DIR PREFIX                              PUT every file of DIR to s3://$DL_S3_BUCKET/PREFIX/
  mirror.py h1 ZIP                                         print the h1: hash of a provider zip

Upload env: DL_S3_ENDPOINT, DL_S3_BUCKET, DL_S3_REGION (default us-east-1), AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
optional DL_S3_ACL (e.g. public-read). Path-style, SigV4. Credentials go only into the signature, never printed.
"""
import base64
import datetime
import hashlib
import hmac
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile

PROJECT = "terraform-provider-h3"
CTYPES = {".json": "application/json", ".zip": "application/zip", ".asc": "application/pgp-keys",
          ".sig": "application/pgp-signature", ".sh": "text/x-shellscript", ".txt": "text/plain"}


def die(msg):
    print("mirror: " + msg, file=sys.stderr)
    sys.exit(1)


def h1(zip_path):
    """Terraform's h1: hash (dirhash Hash1 of the unpacked archive)."""
    files = {}
    with zipfile.ZipFile(zip_path) as z:
        for info in z.infolist():
            if not info.is_dir():
                files[info.filename] = hashlib.sha256(z.read(info)).hexdigest()
    # golang.org/x/mod/sumdb/dirhash.Hash1: lines "<sha256>  <name>\n" in the order of SORTED NAMES
    lines = ["%s  %s\n" % (files[n], n) for n in sorted(files)]
    return "h1:" + base64.b64encode(hashlib.sha256("".join(lines).encode()).digest()).decode()


def zh(path):
    with open(path, "rb") as f:
        return "zh:" + hashlib.sha256(f.read()).hexdigest()


def load_index(src):
    if not src:
        return {"versions": {}}
    try:
        if re.match(r"^https?://", src):
            with urllib.request.urlopen(src, timeout=30) as r:
                return json.load(r)
        with open(src) as f:
            return json.load(f)
    except urllib.error.HTTPError as e:
        if e.code in (403, 404):
            return {"versions": {}}
        raise
    except FileNotFoundError:
        return {"versions": {}}


def build(version, dist, out, index_src):
    version = version.lstrip("v")
    pat = re.compile(r"^%s_%s_([a-z0-9]+)_([a-z0-9]+)\.zip$" % (PROJECT, re.escape(version)))
    archives = {}
    for name in sorted(os.listdir(dist)):
        m = pat.match(name)
        if m:
            p = os.path.join(dist, name)
            archives["%s_%s" % m.groups()] = {"url": name, "hashes": [h1(p), zh(p)]}
    if not archives:
        die("no %s_%s_*.zip in %s" % (PROJECT, version, dist))
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, version + ".json"), "w") as f:
        json.dump({"archives": archives}, f, indent=2, sort_keys=True)
    idx = load_index(index_src)
    idx.setdefault("versions", {})[version] = {}
    with open(os.path.join(out, "index.json"), "w") as f:
        json.dump(idx, f, indent=2, sort_keys=True)
    print("mirror: %s with %d platforms, index has %d versions" % (version, len(archives), len(idx["versions"])))


def _sign(key, msg):
    return hmac.new(key, msg.encode(), hashlib.sha256).digest()


def put(endpoint, bucket, region, key_id, secret, path, data, ctype, acl=None, now=None):
    """One S3 PUT, path-style SigV4. Returns the request (for tests) after sending it unless now is given."""
    u = urllib.parse.urlsplit(endpoint)
    host = u.netloc
    canon_path = "/" + bucket + "/" + urllib.parse.quote(path.lstrip("/"), safe="/-_.~")
    t = now or datetime.datetime.now(datetime.timezone.utc)
    amz_date, day = t.strftime("%Y%m%dT%H%M%SZ"), t.strftime("%Y%m%d")
    payload = hashlib.sha256(data).hexdigest()
    headers = {"content-type": ctype, "host": host, "x-amz-content-sha256": payload, "x-amz-date": amz_date}
    if acl:
        headers["x-amz-acl"] = acl
    signed = ";".join(sorted(headers))
    canon = "\n".join(["PUT", canon_path, "", "".join("%s:%s\n" % (k, headers[k]) for k in sorted(headers)), signed, payload])
    scope = "%s/%s/s3/aws4_request" % (day, region)
    sts = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, hashlib.sha256(canon.encode()).hexdigest()])
    k = _sign(_sign(_sign(_sign(("AWS4" + secret).encode(), day), region), "s3"), "aws4_request")
    sig = hmac.new(k, sts.encode(), hashlib.sha256).hexdigest()
    req = urllib.request.Request("%s://%s%s" % (u.scheme or "https", host, canon_path), data=data, method="PUT")
    for name, v in headers.items():
        if name != "host":
            req.add_header(name, v)
    req.add_header("Authorization", "AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s" % (key_id, scope, signed, sig))
    if now is not None:
        return req
    with urllib.request.urlopen(req, timeout=120) as r:
        if r.status not in (200, 201):
            die("PUT %s: HTTP %s" % (path, r.status))
    return req


def upload(src_dir, prefix):
    env = os.environ
    for v in ("DL_S3_ENDPOINT", "DL_S3_BUCKET", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"):
        if not env.get(v):
            die(v + " is not set")
    n = 0
    paths = [os.path.join(r, f) for r, _, fs in os.walk(src_dir) for f in fs]
    # data first, then the version JSONs, index.json last: a reader never sees an index entry before its files
    paths.sort(key=lambda p: (os.path.basename(p) == "index.json", p.endswith(".json"), p))
    for p in paths:
        name = os.path.basename(p)
        rel = os.path.relpath(p, src_dir).replace(os.sep, "/")
        dest = prefix.strip("/") + "/" + rel if prefix.strip("/") else rel
        with open(p, "rb") as f:
            data = f.read()
        ctype = CTYPES.get(os.path.splitext(name)[1], "application/octet-stream")
        try:
            put(env["DL_S3_ENDPOINT"], env["DL_S3_BUCKET"], env.get("DL_S3_REGION") or "us-east-1",
                env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], dest, data, ctype, env.get("DL_S3_ACL"))
        except urllib.error.HTTPError as e:
            die("PUT %s: HTTP %s %s" % (dest, e.code, e.read()[:300]))
        print("mirror: uploaded %s" % dest)
        n += 1
    if not n:
        die("nothing to upload in " + src_dir)


def main(argv):
    if len(argv) >= 4 and argv[0] == "build":
        idx = None
        if "--index" in argv:
            idx = argv[argv.index("--index") + 1]
        build(argv[1], argv[2], argv[3], idx)
    elif len(argv) == 3 and argv[0] == "upload":
        upload(argv[1], argv[2])
    elif len(argv) == 2 and argv[0] == "h1":
        print(h1(argv[1]))
    else:
        print(__doc__)
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv[1:])
