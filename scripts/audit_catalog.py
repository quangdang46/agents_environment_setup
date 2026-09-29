#!/usr/bin/env python3
"""Audit every github-release coordinate in the catalog against GitHub.

Why this exists, in the shape it actually happened.

A manifest's `sha256:` is the one thing standing between a user and whatever
bytes the release URL serves today. Eight of the 198 coordinates in this catalog
were wrong on 2026-09-30 — across beads, dcg, sbh and uv — and every one of them
would have failed at install time with a checksum mismatch, on a machine that
had done nothing wrong. Three of the four tools were `tested: true`.

scripts/release_probe.py is how those values were originally read, and it is
not wrong to use: it reads the release's own `.sha256` sidecar, then a combined
checksums file, then falls back to GitHub's `digest` field. The gap is that all
three of those are things the *publisher* wrote, and a re-uploaded asset leaves
the old sidecar describing bytes that are no longer there. The value is
trustworthy about what the publisher intended and not necessarily about what the
URL now serves.

So this checks the thing that decides the install: the digest GitHub reports for
the asset, at the tag the manifest PINS. Not `releases/latest` — a moving
target will make a correct manifest look wrong and send someone to "fix" a value
that was right, which is how `uv` lost a correct linux/amd64 hash in this very
session.

Usage:
    python3 scripts/audit_catalog.py            # report, exit 1 on any mismatch
    python3 scripts/audit_catalog.py --quiet    # summary only

Requires `gh` on PATH and authenticated. Exits 1 when any coordinate is wrong,
so it is usable as a gate and not only as a thing to read.
"""

import json
import os
import re
import subprocess
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "tools")

# A platform block: "  darwin:" or "  linux:", then its body at 4 spaces.
PLATFORM_BLOCK = re.compile(r"^  (darwin|linux):\n((?:    .*\n|\n)*)", re.M)
ARCH_ENTRY = re.compile(r"^      (amd64|arm64):\s*(\S+)\s*$", re.M)
SHA_ENTRY = re.compile(r"^      (amd64|arm64):\s*([0-9a-f]{64})\s*$", re.M)

# A sha256 line that is present but not a 64-char hex digest. This is a finding
# in its own right, and it must not be silently skipped: a mutation that
# truncated a hash to 63 characters made the audit report "197 coordinates, 0
# mismatches" — a smaller denominator and a clean bill of health, which is the
# worst possible output for a checker to produce.
MALFORMED_SHA = re.compile(r"^      (amd64|arm64):\s*([0-9a-f]{1,63}|[0-9a-f]{65,})\s*$", re.M)


def section(block, key):
    """The body of a nested `key:` at 4 spaces, or "" when absent."""
    m = re.search(r"^    %s:\n((?:      .*\n|\n)*)" % key, block, re.M)
    return m.group(1) if m else ""


def scalar(block, key):
    m = re.search(r"^    %s:\s*(\S+)" % key, block, re.M)
    return m.group(1) if m else None


def coordinates():
    """Every (tool, os, arch, repo, tag, asset, sha) the catalog declares."""
    for category in sorted(os.listdir(ROOT)):
        catdir = os.path.join(ROOT, category)
        if not os.path.isdir(catdir):
            continue
        for name in sorted(os.listdir(catdir)):
            path = os.path.join(catdir, name, "tool.yaml")
            if not os.path.exists(path):
                continue
            text = open(path).read()
            if "strategy: github-release" not in text:
                continue
            for m in PLATFORM_BLOCK.finditer(text):
                os_name, block = m.group(1), m.group(2)
                tag, repo = scalar(block, "release_tag"), scalar(block, "repository")
                # No release_tag means `releases/latest`, which cannot be
                # audited at all: the bytes move and a mismatch is
                # indistinguishable from a stale manifest. That is a finding in
                # its own right, reported by the caller.
                if not tag or not repo:
                    continue
                assets = dict(ARCH_ENTRY.findall(section(block, "asset")))
                sha_block = section(block, "sha256")
                shas = dict(SHA_ENTRY.findall(sha_block))
                broken = dict(MALFORMED_SHA.findall(sha_block))
                for arch in sorted(set(assets) & set(broken) - set(shas)):
                    yield (f"{category}/{name}", os_name, arch, repo, tag,
                           assets[arch], broken[arch])
                for arch in sorted(set(assets) & set(shas)):
                    yield (f"{category}/{name}", os_name, arch, repo, tag,
                           assets[arch], shas[arch])


def unpinned():
    """github-release targets with no release_tag, which cannot be pinned."""
    found = []
    for category in sorted(os.listdir(ROOT)):
        catdir = os.path.join(ROOT, category)
        if not os.path.isdir(catdir):
            continue
        for name in sorted(os.listdir(catdir)):
            path = os.path.join(catdir, name, "tool.yaml")
            if not os.path.exists(path):
                continue
            text = open(path).read()
            if "strategy: github-release" not in text or "release_tag:" in text:
                continue
            found.append(f"{category}/{name}")
    return found


def release_assets(repo, tag, cache):
    key = (repo, tag)
    if key not in cache:
        out = subprocess.run(
            ["gh", "api", f"repos/{repo}/releases/tags/{tag}"],
            capture_output=True, text=True,
        ).stdout
        cache[key] = json.loads(out) if out.strip() else None
    rel = cache[key]
    if rel is None:
        return None
    return {a["name"]: a.get("digest", "") for a in rel.get("assets", [])}


def main():
    quiet = "--quiet" in sys.argv
    cache = {}
    rows = list(coordinates())
    bad = 0

    for tool, os_name, arch, repo, tag, asset, declared in rows:
        digests = release_assets(repo, tag, cache)
        if digests is None:
            if not quiet:
                print(f"TAG-MISSING   {tool:34s} {os_name}/{arch}  {repo}@{tag}")
            bad += 1
            continue
        actual = digests.get(asset, "").replace("sha256:", "")
        if not actual:
            if not quiet:
                print(f"ASSET-MISSING {tool:34s} {os_name}/{arch}  {asset!r} "
                      f"is not in {repo}@{tag}")
            bad += 1
        elif actual != declared:
            if not quiet:
                print(f"SHA-WRONG     {tool:34s} {os_name}/{arch}  {asset}")
                print(f"               declared {declared}")
                print(f"               upstream {actual}")
            bad += 1

    loose = unpinned()
    if loose and not quiet:
        print(f"\nUNPINNED      {len(loose)} github-release target(s) with no "
              f"release_tag, which cannot be audited:")
        for t in loose:
            print(f"               {t}")

    print(f"coordinates: {len(rows)}  mismatches: {bad}  unpinned: {len(loose)}")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
