#!/usr/bin/env python3
"""Release autopilot for this repository (the same script lives in h3cli and h3terraform).

CHANGELOG.md is the source of truth. Entries are written under `## [Unreleased]` (or `## [X.Y.Z] - unreleased`
when a version is already planned) in the usual Keep a Changelog subsections.

  release.py next              print the next version: bump from the subsections of the unreleased section
                               (Breaking changes/Removed -> major, minor before 1.0; Added/Changed/Deprecated -> minor;
                               Fixed/Security/other -> patch), never below a version written in the heading
  release.py prepare [--dry-run] [--version X.Y.Z]
                               date the unreleased section, add a new empty `## [Unreleased]`, push branch
                               release/vX.Y.Z and open ONE merge request (owner or autopilot merges it)
  release.py tag [--dry-run]   on main after the release MR: create the protected tag vX.Y.Z of the newest dated
                               section if it does not exist yet; the tag pipeline builds and publishes
  release.py check-tag vX.Y.Z  tag pipeline guard: dated section exists, tag commit is on the default branch
  release.py notes X.Y.Z       print the section body (release notes)
  release.py changelog [--write]
                               entries from conventional commits since the last tag (feat -> Added, fix -> Fixed,
                               perf/refactor/revert -> Changed, `!` or BREAKING CHANGE -> Breaking changes; docs, ci,
                               chore, test, build, style are skipped); --write puts them into an EMPTY unreleased
                               section (hand-written entries always win)
  prepare --from-commits       the same before preparing: the release MR also works when nobody wrote CHANGELOG

GitLab: CI_API_V4_URL, CI_PROJECT_ID, CI_DEFAULT_BRANCH from CI; RELEASE_BOT_TOKEN (project access token, protected
variable) for push/MR/tag. Tokens are sent only in headers or the git credential helper and are never printed.
Stdlib only: runs in the goreleaser and python images.
"""
import argparse
import datetime
import json
import os
import re
import subprocess
import sys
import urllib.parse
import urllib.request

CHANGELOG = "CHANGELOG.md"
HEAD_RE = re.compile(r"^## \[(?P<ver>[^\]]+)\](?:\s*-\s*(?P<date>.+))?\s*$")
SEMVER_RE = re.compile(r"^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$")
MAJOR = ("breaking", "removed")
MINOR = ("added", "changed", "deprecated")


def die(msg):
    print("release: " + msg, file=sys.stderr)
    sys.exit(1)


def git(*args, check=True):
    r = subprocess.run(["git"] + list(args), capture_output=True, text=True)
    if check and r.returncode != 0:
        die("git %s: %s" % (args[0], r.stderr.strip()))
    return r.stdout.strip()


# ------------------------------------------------------------------ changelog


def sections(text):
    """[(heading_line_index, version, date_or_None)]"""
    out = []
    for i, line in enumerate(text.splitlines()):
        m = HEAD_RE.match(line)
        if m:
            date = (m.group("date") or "").strip() or None
            out.append((i, m.group("ver").strip(), date))
    return out


def is_unreleased(ver, date):
    return ver.lower() == "unreleased" or (date or "").lower() == "unreleased"


def section_body(text, idx):
    lines = text.splitlines()
    body = []
    for line in lines[idx + 1:]:
        if HEAD_RE.match(line):
            break
        body.append(line)
    return "\n".join(body).strip("\n")


def parse(v):
    m = SEMVER_RE.match(v.lstrip("v"))
    if not m:
        return None
    return tuple(int(x) for x in m.groups()[:3])


def fmt(t):
    return "%d.%d.%d" % t


def bump(last, kind):
    major, minor, patch = last
    if kind == "major":
        return (major, minor + 1, 0) if major == 0 else (major + 1, 0, 0)
    if kind == "minor":
        return (major, minor + 1, 0)
    return (major, minor, patch + 1)


def bump_kind(body):
    subs = [l[4:].strip().lower() for l in body.splitlines() if l.startswith("### ")]
    if any(s.startswith(MAJOR) for s in subs):
        return "major"
    if any(s.startswith(MINOR) for s in subs):
        return "minor"
    return "patch"


def last_released(text):
    for _, ver, date in sections(text):
        if not is_unreleased(ver, date) and parse(ver):
            return parse(ver)
    tags = [parse(t) for t in git("tag", "--list", "v*", check=False).split()]
    tags = [t for t in tags if t]
    return max(tags) if tags else (0, 0, 0)


def unreleased(text):
    for idx, ver, date in sections(text):
        if is_unreleased(ver, date):
            return idx, ver, date
    return None


def next_version(text):
    u = unreleased(text)
    if not u:
        die("no unreleased section in %s: nothing to release" % CHANGELOG)
    idx, ver, _ = u
    body = section_body(text, idx)
    if not body.strip():
        die("the unreleased section is empty: nothing to release")
    kind = bump_kind(body)
    want = bump(last_released(text), kind)
    planned = parse(ver)
    if planned and planned > want:
        want = planned  # a version written in the heading wins if it is higher
    return fmt(want), kind, idx


def dated(text, version):
    """Index of the dated section of version. A pre-release (0.3.0-rc.1) uses the section of 0.3.0,
    which may still be unreleased: release candidates are cut before the release MR."""
    base, pre = version.split("-", 1)[0], "-" in version
    for idx, ver, date in sections(text):
        if ver == version and not is_unreleased(ver, date):
            return idx
        if pre and parse(ver) == parse(base):
            return idx
    if pre:
        u = unreleased(text)
        return u[0] if u else None
    return None


# ------------------------------------------------------------------ conventional commits

CC_RE = re.compile(r"^(?P<type>[a-zA-Z]+)(?:\((?P<scope>[^)]*)\))?(?P<bang>!)?:\s*(?P<desc>\S.*)$")
CC_SECTIONS = {"feat": "Added", "fix": "Fixed", "perf": "Changed", "refactor": "Changed", "revert": "Changed",
               "security": "Security", "sec": "Security", "deprecate": "Deprecated"}
CC_ORDER = ["Breaking changes", "Added", "Changed", "Deprecated", "Fixed", "Security"]


def conventional(commits):
    """commits: [(subject, body)] -> {section: [line]}; non-conventional and skipped types are ignored."""
    out = {}
    for subject, body in commits:
        m = CC_RE.match(subject.strip())
        if not m:
            continue
        typ = m.group("type").lower()
        breaking = bool(m.group("bang")) or re.search(r"^BREAKING[ -]CHANGE:", body or "", re.M) is not None
        sec = "Breaking changes" if breaking else CC_SECTIONS.get(typ)
        if not sec:
            continue
        desc = m.group("desc").strip()
        if m.group("scope"):
            desc = "%s: %s" % (m.group("scope"), desc)
        if desc not in out.setdefault(sec, []):
            out[sec].append(desc)
    return out


def render(entries):
    parts = []
    for sec in CC_ORDER:
        if entries.get(sec):
            parts.append("### %s\n\n%s" % (sec, "\n".join("- " + e for e in entries[sec])))
    return "\n\n".join(parts)


def commits_since_last_tag():
    last = git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", check=False)
    rng = [last + "..HEAD"] if last else ["HEAD"]
    raw = git("log", "--no-merges", "--format=%s%x1f%b%x1e", *rng, check=False)
    return [tuple((c.split("\x1f") + [""])[:2]) for c in raw.split("\x1e") if c.strip()]


def fill_unreleased(text, body):
    """Put body into an empty unreleased section (created on top when missing). Returns the new text."""
    lines = text.splitlines()
    u = unreleased(text)
    if u is None:
        first = next((i for i, l in enumerate(lines) if HEAD_RE.match(l)), len(lines))
        lines[first:first] = ["## [Unreleased]", ""]
        text = "\n".join(lines) + "\n"
        u = unreleased(text)
        lines = text.splitlines()
    idx = u[0]
    if section_body(text, idx).strip():
        return text
    end = idx + 1
    while end < len(lines) and not HEAD_RE.match(lines[end]):
        end += 1
    lines[idx + 1:end] = ["", body, ""]
    return "\n".join(lines) + "\n"


# ------------------------------------------------------------------ gitlab


def api(method, path, data=None):
    base = os.environ.get("CI_API_V4_URL") or die("CI_API_V4_URL is not set")
    token = os.environ.get("RELEASE_BOT_TOKEN") or die("RELEASE_BOT_TOKEN is not set")
    req = urllib.request.Request(base.rstrip("/") + path, method=method)
    req.add_header("PRIVATE-TOKEN", token)
    body = None
    if data is not None:
        body = json.dumps(data).encode()
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, body, timeout=30) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        return e.code, None


def project():
    return urllib.parse.quote(os.environ.get("CI_PROJECT_ID") or die("CI_PROJECT_ID is not set"), safe="")


def push(branch):
    url = os.environ.get("CI_REPOSITORY_URL", "")
    host = urllib.parse.urlsplit(url).hostname or die("CI_REPOSITORY_URL is not set")
    remote = "https://%s/%s.git" % (host, os.environ["CI_PROJECT_PATH"])
    helper = '!f(){ echo username=release-bot; echo "password=$RELEASE_BOT_TOKEN"; }; f'
    git("-c", "credential.helper=", "-c", "credential.helper=" + helper, "push", remote, "HEAD:refs/heads/" + branch)


# ------------------------------------------------------------------ commands


def cmd_next(_a):
    text = open(CHANGELOG).read()
    v, kind, _ = next_version(text)
    print("%s %s" % (v, kind))


def cmd_notes(a):
    text = open(CHANGELOG).read()
    idx = dated(text, a.version.lstrip("v"))
    if idx is None:
        die("no dated section [%s] in %s" % (a.version, CHANGELOG))
    print(section_body(text, idx))


def cmd_check_tag(a):
    v = a.tag.lstrip("v")
    if not parse(v) or not a.tag.startswith("v"):
        die("tag %s is not vX.Y.Z[-pre]" % a.tag)
    text = open(CHANGELOG).read()
    if dated(text, v) is None:
        die("tag %s: CHANGELOG has no dated section [%s] (release MR not merged?)" % (a.tag, v))
    default = os.environ.get("CI_DEFAULT_BRANCH", "main")
    git("fetch", "--quiet", "origin", default, check=False)
    if subprocess.run(["git", "merge-base", "--is-ancestor", "HEAD", "origin/" + default]).returncode != 0:
        die("tag %s points to a commit that is not on %s" % (a.tag, default))
    print("tag %s ok" % a.tag)


def cmd_changelog(a):
    body = render(conventional(commits_since_last_tag()))
    if not body:
        print("(no conventional commits since the last tag)")
        return
    print(body)
    if a.write:
        text = open(CHANGELOG).read()
        new = fill_unreleased(text, body)
        if new == text:
            print("release: the unreleased section already has entries, CHANGELOG.md not changed", file=sys.stderr)
        else:
            open(CHANGELOG, "w").write(new)


def cmd_prepare(a):
    text = open(CHANGELOG).read()
    if a.from_commits:
        body = render(conventional(commits_since_last_tag()))
        if body:
            text = fill_unreleased(text, body)
    v, kind, idx = next_version(text)
    if a.version:
        if not parse(a.version) or parse(a.version) < parse(v):
            die("--version %s is below the required %s (%s change)" % (a.version, v, kind))
        v = a.version
    today = datetime.date.today().isoformat()
    lines = text.splitlines()
    lines[idx:idx + 1] = ["## [Unreleased]", "", "## [%s] - %s" % (v, today)]
    new = "\n".join(lines) + "\n"
    branch = "release/v" + v
    print("release v%s (%s change), branch %s" % (v, kind, branch))
    if a.dry_run:
        print(section_body(new, new.splitlines().index("## [%s] - %s" % (v, today))))
        return
    pid = project()
    status, _ = api("GET", "/projects/%s/repository/tags/v%s" % (pid, v))
    if status == 200:
        die("tag v%s already exists" % v)
    status, mrs = api("GET", "/projects/%s/merge_requests?state=opened&source_branch=%s" % (pid, urllib.parse.quote(branch, safe="")))
    if status == 200 and mrs:
        print("MR already open: %s" % mrs[0]["web_url"])
        return
    open(CHANGELOG, "w").write(new)
    git("checkout", "-B", branch)
    git("-c", "user.name=release-bot", "-c", "user.email=release-bot@h3llo.cloud", "commit", "-q", "-am",
        "release: v%s\n\nCHANGELOG: the unreleased section becomes [%s] - %s." % (v, v, today))
    push(branch)
    notes = section_body(new, new.splitlines().index("## [%s] - %s" % (v, today)))
    desc = ("Релиз **v%s** (%s). После мержа job `release-tag` на `%s` ставит защищённый тег, "
            "пайплайн тега собирает и публикует артефакты.\n\n## Что войдёт\n\n%s\n"
            % (v, kind, os.environ.get("CI_DEFAULT_BRANCH", "main"), notes))
    status, mr = api("POST", "/projects/%s/merge_requests" % pid, {
        "source_branch": branch, "target_branch": os.environ.get("CI_DEFAULT_BRANCH", "main"),
        "title": "release: v%s" % v, "description": desc, "labels": "release", "remove_source_branch": True})
    if status not in (200, 201):
        die("cannot open MR (HTTP %s)" % status)
    print("MR: %s" % mr["web_url"])


def cmd_tag(a):
    text = open(CHANGELOG).read()
    for idx, ver, date in sections(text):
        if not is_unreleased(ver, date) and parse(ver):
            break
    else:
        die("no dated section in %s" % CHANGELOG)
    tag = "v" + ver
    sha = os.environ.get("CI_COMMIT_SHA") or git("rev-parse", "HEAD")
    if a.dry_run:
        print("would tag %s at %s" % (tag, sha))
        return
    pid = project()
    status, _ = api("GET", "/projects/%s/repository/tags/%s" % (pid, tag))
    if status == 200:
        print("tag %s exists, nothing to do" % tag)
        return
    status, _ = api("POST", "/projects/%s/repository/tags" % pid,
                    {"tag_name": tag, "ref": sha, "message": "%s %s" % (os.environ.get("CI_PROJECT_NAME", ""), tag)})
    if status not in (200, 201):
        die("cannot create tag %s (HTTP %s)" % (tag, status))
    print("created tag %s at %s" % (tag, sha))


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("next").set_defaults(fn=cmd_next)
    s = sub.add_parser("notes")
    s.add_argument("version")
    s.set_defaults(fn=cmd_notes)
    s = sub.add_parser("check-tag")
    s.add_argument("tag")
    s.set_defaults(fn=cmd_check_tag)
    s = sub.add_parser("prepare")
    s.add_argument("--dry-run", action="store_true")
    s.add_argument("--version")
    s.add_argument("--from-commits", action="store_true")
    s.set_defaults(fn=cmd_prepare)
    s = sub.add_parser("changelog")
    s.add_argument("--write", action="store_true")
    s.set_defaults(fn=cmd_changelog)
    s = sub.add_parser("tag")
    s.add_argument("--dry-run", action="store_true")
    s.set_defaults(fn=cmd_tag)
    a = p.parse_args()
    a.fn(a)


if __name__ == "__main__":
    main()
