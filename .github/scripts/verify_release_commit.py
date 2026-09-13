#!/usr/bin/env python3
"""Require the checked-out and current remote release commit to match validation."""

import argparse
from pathlib import Path
import re
import subprocess

from prepare_release_notes import parse_tag


def verify_release_commit(tag: str, commit: str, directory: Path = Path(".")) -> None:
    parse_tag(tag)
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("a full validated commit SHA is required")
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=directory, text=True).strip()
    if head != commit:
        raise ValueError("checked-out commit did not pass validation")
    ref = "refs/tags/" + tag
    remote = subprocess.check_output(
        ["git", "ls-remote", "--exit-code", "--tags", "origin", ref, ref + "^{}"],
        cwd=directory, text=True, timeout=30,
    )
    refs = {name: sha for sha, name in (line.split() for line in remote.splitlines())}
    if refs.get(ref + "^{}", refs.get(ref)) != commit:
        raise ValueError("release tag no longer points to the validated commit")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--commit", required=True)
    args = parser.parse_args()
    verify_release_commit(args.tag, args.commit)
