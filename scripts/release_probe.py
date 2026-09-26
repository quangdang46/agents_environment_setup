#!/usr/bin/env python3
"""Fetch real release assets and checksums for a tool.yaml.

A checksum that is invented is worse than no checksum: it fails at install
time with a confusing error, and it looks like the catalog was verified. So
this script reads the actual release and prints the actual digest, and prints
nothing at all when it cannot find one.

Usage:
    release_probe.py <owner/repo> [version]
    release_probe.py --batch <file>     # one owner/repo[@version] per line

Output is a YAML fragment for a github-release Target, or a diagnostic on
stderr explaining why the tool is not eligible.
"""
import json
import re
import sys
import urllib.error
import urllib.request

API = "https://api.github.com/repos/{repo}/releases/tags/{tag}"
LATEST = "https://api.github.com/repos/{repo}/releases/latest"

# How upstream names each Go arch, per platform. These are the conventions we
# have actually observed; a repo that uses something else simply won't match
# and gets reported rather than guessed at.
ARCH_TOKENS = {
    ("darwin", "arm64"): ["aarch64-apple-darwin", "arm64-apple-darwin", "aarch64-apple", "darwin-arm64", "macos-arm64", "apple-darwin-aarch64", "darwin_arm64", "macos_arm64", "osx-arm64"],
    ("darwin", "amd64"): ["x86_64-apple-darwin", "amd64-apple-darwin", "x86_64-apple", "darwin-amd64", "macos-amd64", "apple-darwin-x86_64", "x86_64-apple-darwin-gnu", "darwin_amd64", "macos_amd64", "osx-amd64"],
    ("linux", "arm64"): ["aarch64-unknown-linux-gnu", "aarch64-unknown-linux-musl", "aarch64-linux-gnu", "arm64-unknown-linux-gnu", "linux-arm64", "aarch64-unknown-linux", "linux_arm64"],
    ("linux", "amd64"): ["x86_64-unknown-linux-gnu", "x86_64-unknown-linux-musl", "x86_64-linux-gnu", "amd64-unknown-linux-linux-gnu", "linux-amd64", "x86_64-unknown-linux", "linux_amd64"],
}
ARCH_SUFFIX = {"tar.gz", ".tgz", ".zip", ".tar.xz", ".gz", ".xz"}


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "aes-catalog", "Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(req, timeout=25) as r:
        return r.read()


def resolve(repo, version):
    if version:
        return version.lstrip("v"), API.format(repo=repo, tag=version)
    data = json.loads(fetch(LATEST.format(repo=repo)))
    return data["tag_name"], ""


def release(repo, version):
    tag, url = resolve(repo, version)
    data = json.loads(fetch(url) if url else fetch(LATEST.format(repo=repo)))
    return data


def pick_asset(assets, platform, arch):
    """Return the archive asset for a platform+arch, or None.

    Refuses anything that is not a plain archive: installers, .dmg, .deb and
    .rpm are not what the github-release strategy extracts, and silently
    substituting one would produce a tool.yaml that installs nothing.
    """
    names = [a["name"] for a in assets]
    archives = [n for n in names if n.endswith(tuple(ARCH_SUFFIX))]
    for token in ARCH_TOKENS[(platform, arch)]:
        for name in archives:
            low = name.lower()
            if token in low and not _wrong_platform(low, platform):
                return name
    return None


def _wrong_platform(name, platform):
    if platform == "darwin":
        return "linux" in name or "windows" in name
    return "darwin" in name or "apple" in name or "windows" in name


def checksum_for(assets, name):
    """Read the digest from the release's own checksum file.

    Looks for a per-asset .sha256 sidecar first, then a combined checksums.txt
    / SHA256SUMS, then falls back to GitHub's digest field. Returns None rather
    than guessing.
    """
    by_name = {a["name"]: a for a in assets}

    sidecar = by_name.get(name + ".sha256") or by_name.get(name + ".sha256sum")
    if sidecar:
        try:
            text = fetch(sidecar["browser_download_url"]).decode()
            m = re.search(r"\b([0-9a-f]{64})\b", text)
            if m:
                return m.group(1)
        except Exception as e:  # noqa: BLE001 - diagnostics only
            print(f"    sidecar unreadable: {e}", file=sys.stderr)

    # Many projects version the checksum file (fzf_0.74.4_checksums.txt), so
    # match on the word rather than a fixed name.
    for asset in assets:
        low = asset["name"].lower()
        if not ("checksum" in low or "sha256sum" in low or "sha256s" in low):
            continue
        if low.endswith((".sha256", ".sha256sum", ".asc", ".sig", ".pem")):
            continue
        try:
            text = fetch(asset["browser_download_url"]).decode()
        except Exception as e:  # noqa: BLE001 - diagnostics only
            print(f"    {asset['name']} unreadable: {e}", file=sys.stderr)
            continue
        for line in text.splitlines():
            parts = line.split()
            if len(parts) >= 2 and parts[-1].lstrip("*") == name:
                m = re.fullmatch(r"([0-9a-fA-F]{64})", parts[0])
                if m:
                    return m.group(1).lower()

    digest = by_name.get(name, {}).get("digest") or ""
    m = re.fullmatch(r"sha256:([0-9a-f]{64})", digest)
    return m.group(1) if m else None


def probe(repo, version=None):
    try:
        rel = release(repo, version)
    except urllib.error.HTTPError as e:
        return None, f"no release ({e.code})"
    except Exception as e:  # noqa: BLE001
        return None, f"fetch failed: {e}"

    assets = rel.get("assets", [])
    if not assets:
        return None, f"release {rel['tag_name']} has no assets"

    found, sums = {}, {}
    for platform in ("darwin", "linux"):
        for arch in ("amd64", "arm64"):
            name = pick_asset(assets, platform, arch)
            if not name:
                continue
            digest = checksum_for(assets, name)
            if not digest:
                print(f"    {platform}/{arch}: found {name} but NO checksum", file=sys.stderr)
                continue
            found[(platform, arch)] = name
            sums[(platform, arch)] = digest

    if not found:
        return None, f"no matching archives in {rel['tag_name']}"
    return {"tag": rel["tag_name"], "assets": found, "sums": sums}, None


def emit(repo, info):
    tag = info["tag"]
    out = [f"# {repo} @ {tag}"]
    for platform in ("darwin", "linux"):
        for arch in ("amd64", "arm64"):
            key = (platform, arch)
            if key not in info["assets"]:
                continue
            out.append(f"{platform}.asset.{arch} = {info['assets'][key]}")
            out.append(f"{platform}.sha256.{arch} = {info['sums'][key]}")
    return "\n".join(out)


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        return 1
    specs = []
    if args[0] == "--batch":
        with open(args[1]) as fh:
            specs = [ln.strip() for ln in fh if ln.strip() and not ln.startswith("#")]
    else:
        specs = [args[0] if "@" not in args[0] else args[0]] if len(args) == 1 else [args[0] + "@" + args[1]]

    for spec in specs:
        repo, _, version = spec.partition("@")
        version = version or None
        print(f"== {repo} {('@' + version) if version else ''}", file=sys.stderr)
        info, err = probe(repo, version)
        if err:
            # Diagnostics go to stderr so stdout stays machine-parseable: a
            # caller generating manifests should never have to skip a block
            # that a human-facing message landed in.
            print(f"  SKIP {repo}: {err}", file=sys.stderr)
            continue
        print(emit(repo, info))
        print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
