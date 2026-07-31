#!/usr/bin/env python3
import argparse
import re
from pathlib import Path


SEMVER_TAG = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
PLACEHOLDER = re.compile(r"\{\{[^{}]+\}\}")


class ReleaseNotesError(ValueError):
    pass


def parse_tag(tag: str) -> tuple[int, int, int]:
    match = SEMVER_TAG.fullmatch(tag)
    if not match:
        raise ReleaseNotesError(f"release tag must match vX.Y.Z, got {tag!r}")
    major, minor, patch = match.groups()
    return int(major), int(minor), int(patch)


def curated_notes_path(tag: str, curated_dir: Path) -> Path:
    parse_tag(tag)
    return curated_dir / f"{tag}.md"


def validate_detailed_notes(
    notes: str,
    *,
    tag: str,
    image: str,
    repository: str,
) -> None:
    version = tag.removeprefix("v")
    title = f"# Fileament {tag}"
    required_sections = (
        "> [!WARNING]",
        "## Highlights",
        "## Upgrade notes",
        "## Changes by pull request",
    )

    if notes.splitlines()[:1] != [title]:
        raise ReleaseNotesError(f"detailed notes must start with {title!r}")
    if PLACEHOLDER.search(notes):
        raise ReleaseNotesError("detailed notes still contain template placeholders")

    positions = []
    for marker in required_sections:
        position = notes.find(marker)
        if position < 0:
            raise ReleaseNotesError(f"detailed notes are missing {marker!r}")
        positions.append(position)
    if positions != sorted(positions):
        raise ReleaseNotesError("detailed release-note sections are out of order")

    summary = notes[len(title) : positions[0]].strip()
    highlights = notes[positions[1] + len(required_sections[1]) : positions[2]].strip()
    upgrade_notes = notes[positions[2] + len(required_sections[2]) : positions[3]].strip()
    if not summary or not highlights or not upgrade_notes:
        raise ReleaseNotesError("summary, highlights, and upgrade notes must not be empty")

    pull_command = f"docker pull {image.lower()}:{version}"
    if pull_command not in notes:
        raise ReleaseNotesError(f"detailed notes must include {pull_command!r}")

    compare_pattern = re.compile(
        rf"\*\*Full changelog:\*\* https://github\.com/{re.escape(repository)}/compare/v[0-9]+\.[0-9]+\.[0-9]+\.\.\.{re.escape(tag)}\s*\Z"
    )
    compare = compare_pattern.search(notes)
    if compare is None:
        raise ReleaseNotesError("detailed notes must end with a full changelog comparison to the release tag")
    ledger = notes[positions[3] + len(required_sections[3]) : compare.start()].strip()
    if not re.search(r"^### \[[^]]+\]\(https://github\.com/[^)]+/pull/[0-9]+\)", ledger, re.MULTILINE):
        raise ReleaseNotesError("detailed notes must include at least one linked pull request")


def load_detailed_notes(
    *,
    tag: str,
    image: str,
    repository: str,
    curated_dir: Path,
) -> str:
    path = curated_notes_path(tag, curated_dir)
    if not path.is_file():
        raise ReleaseNotesError(
            f"{tag} requires curated detailed notes at {path}; copy TEMPLATE.md and complete every section before tagging"
        )
    notes = path.read_text(encoding="utf-8").rstrip() + "\n"
    validate_detailed_notes(notes, tag=tag, image=image, repository=repository)
    return notes


def render_patch_notes(*, tag: str, image: str, repository: str, generated_notes: str) -> str:
    version = tag.removeprefix("v")
    changes = generated_notes.strip()
    if not changes:
        raise ReleaseNotesError("patch releases require GitHub-generated notes")
    compare_pattern = re.compile(
        rf"\*\*Full Changelog\*\*\s*: https://github\.com/{re.escape(repository)}/compare/v[0-9]+\.[0-9]+\.[0-9]+\.\.\.{re.escape(tag)}\s*\Z"
    )
    if not compare_pattern.search(changes):
        raise ReleaseNotesError("patch notes must end with GitHub's full changelog comparison to the release tag")

    changes, replacements = re.subn(
        r"^## What's Changed\s*$",
        "## Fixes and improvements",
        changes,
        count=1,
        flags=re.MULTILINE,
    )
    if replacements == 0:
        changes = f"## Fixes and improvements\n\n{changes}"
    changes = re.sub(
        r"\*\*Full Changelog\*\*\s*:",
        "**Full changelog:**",
        changes,
    )

    return f"""# Fileament {tag}

This patch release contains focused fixes and improvements.

## Container image

```sh
docker pull {image.lower()}:{version}
```

Published for `linux/amd64` and `linux/arm64`. The `latest` tag points to this stable release.

{changes}
"""


def build_release_notes(
    *,
    tag: str,
    image: str,
    repository: str,
    curated_dir: Path,
    generated_notes: str | None = None,
) -> str:
    _, _, patch = parse_tag(tag)
    if patch == 0:
        return load_detailed_notes(
            tag=tag,
            image=image,
            repository=repository,
            curated_dir=curated_dir,
        )
    return render_patch_notes(
        tag=tag,
        image=image,
        repository=repository,
        generated_notes=generated_notes or "",
    )


def validate_policy(
    *,
    tag: str,
    image: str,
    repository: str,
    curated_dir: Path,
) -> None:
    _, _, patch = parse_tag(tag)
    if patch == 0:
        load_detailed_notes(
            tag=tag,
            image=image,
            repository=repository,
            curated_dir=curated_dir,
        )


def main() -> None:
    parser = argparse.ArgumentParser(description="Validate and prepare Fileament release notes")
    parser.add_argument("--tag", required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--curated-dir", type=Path, default=Path(".github/release-notes"))
    parser.add_argument("--generated-notes-file", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--validate-only", action="store_true")
    args = parser.parse_args()

    try:
        if args.validate_only:
            validate_policy(
                tag=args.tag,
                image=args.image,
                repository=args.repository,
                curated_dir=args.curated_dir,
            )
            return

        if args.output is None:
            raise ReleaseNotesError("--output is required unless --validate-only is used")
        generated_notes = ""
        if args.generated_notes_file is not None:
            generated_notes = args.generated_notes_file.read_text(encoding="utf-8")
        notes = build_release_notes(
            tag=args.tag,
            image=args.image,
            repository=args.repository,
            curated_dir=args.curated_dir,
            generated_notes=generated_notes,
        )
        args.output.write_text(notes, encoding="utf-8")
    except (OSError, ReleaseNotesError) as error:
        parser.exit(1, f"release note policy error: {error}\n")


if __name__ == "__main__":
    main()
