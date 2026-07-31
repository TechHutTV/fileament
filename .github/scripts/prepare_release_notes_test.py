import tempfile
import unittest
from pathlib import Path

from prepare_release_notes import ReleaseNotesError, build_release_notes


IMAGE = "ghcr.io/techhuttv/fileament"
REPOSITORY = "TechHutTV/fileament"


def detailed_notes(tag: str) -> str:
    version = tag.removeprefix("v")
    return f"""# Fileament {tag}

A detailed summary of the release.

> [!WARNING]
> Back up `/data` before updating.

## Highlights

### A user-facing improvement

Details about the improvement and why it matters.

## Upgrade notes

No database migration is required.

```sh
docker pull {IMAGE}:{version}
```

## Changes by pull request

### [feat: example](https://github.com/{REPOSITORY}/pull/99) by [@TechHutTV](https://github.com/TechHutTV)

- Added the example behavior.

**Full changelog:** https://github.com/{REPOSITORY}/compare/v1.2.0...{tag}
"""


class BuildReleaseNotesTests(unittest.TestCase):
    def test_minor_release_requires_versioned_curated_notes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ReleaseNotesError, r"v1\.3\.0\.md"):
                build_release_notes(
                    tag="v1.3.0",
                    image=IMAGE,
                    repository=REPOSITORY,
                    curated_dir=Path(directory),
                )

    def test_minor_release_uses_valid_detailed_notes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "v1.3.0.md")
            path.write_text(detailed_notes("v1.3.0"), encoding="utf-8")

            notes = build_release_notes(
                tag="v1.3.0",
                image=IMAGE,
                repository=REPOSITORY,
                curated_dir=Path(directory),
            )

            self.assertEqual(notes, detailed_notes("v1.3.0"))

    def test_minor_release_rejects_incomplete_or_unfinished_notes(self) -> None:
        notes_without_pull_requests = detailed_notes("v1.3.0")
        ledger_start = notes_without_pull_requests.index("## Changes by pull request") + len("## Changes by pull request")
        ledger_end = notes_without_pull_requests.index("**Full changelog:**")
        notes_without_pull_requests = notes_without_pull_requests[:ledger_start] + "\n\n" + notes_without_pull_requests[ledger_end:]
        invalid_notes = {
            "missing highlights": detailed_notes("v1.3.0").replace("## Highlights", "## Overview"),
            "missing upgrade notes": detailed_notes("v1.3.0").replace("## Upgrade notes", "## Updating"),
            "missing pull requests": detailed_notes("v1.3.0").replace("## Changes by pull request", "## Changes"),
            "missing warning": detailed_notes("v1.3.0").replace("> [!WARNING]", "> Note"),
            "empty pull request ledger": notes_without_pull_requests,
            "template placeholder": detailed_notes("v1.3.0") + "\n{{FINISH_ME}}\n",
        }
        for label, body in invalid_notes.items():
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                Path(directory, "v1.3.0.md").write_text(body, encoding="utf-8")
                with self.assertRaises(ReleaseNotesError):
                    build_release_notes(
                        tag="v1.3.0",
                        image=IMAGE,
                        repository=REPOSITORY,
                        curated_dir=Path(directory),
                    )

    def test_patch_release_renders_concise_generated_notes(self) -> None:
        generated = """## What's Changed
* fix: correct a regression by @TechHutTV in https://github.com/TechHutTV/fileament/pull/14

**Full Changelog**: https://github.com/TechHutTV/fileament/compare/v1.2.0...v1.2.1
"""

        notes = build_release_notes(
            tag="v1.2.1",
            image="ghcr.io/TechHutTV/fileament",
            repository=REPOSITORY,
            curated_dir=Path("unused"),
            generated_notes=generated,
        )

        self.assertIn("# Fileament v1.2.1", notes)
        self.assertIn("This patch release contains focused fixes and improvements.", notes)
        self.assertIn("docker pull ghcr.io/techhuttv/fileament:1.2.1", notes)
        self.assertIn("## Fixes and improvements", notes)
        self.assertNotIn("## What's Changed", notes)
        self.assertIn("**Full changelog:**", notes)

    def test_patch_release_rejects_missing_or_wrong_changelog_range(self) -> None:
        invalid_notes = {
            "missing comparison": "## What's Changed\n* fix: example",
            "wrong target tag": (
                "## What's Changed\n* fix: example\n\n"
                "**Full Changelog**: https://github.com/TechHutTV/fileament/compare/v1.2.0...v1.2.2"
            ),
            "wrong repository": (
                "## What's Changed\n* fix: example\n\n"
                "**Full Changelog**: https://github.com/example/fileament/compare/v1.2.0...v1.2.1"
            ),
        }
        for label, generated in invalid_notes.items():
            with self.subTest(label=label), self.assertRaises(ReleaseNotesError):
                build_release_notes(
                    tag="v1.2.1",
                    image=IMAGE,
                    repository=REPOSITORY,
                    curated_dir=Path("unused"),
                    generated_notes=generated,
                )

    def test_rejects_non_semver_tags(self) -> None:
        with self.assertRaises(ReleaseNotesError):
            build_release_notes(
                tag="release-1.2.1",
                image=IMAGE,
                repository=REPOSITORY,
                curated_dir=Path("unused"),
                generated_notes="changes",
            )


if __name__ == "__main__":
    unittest.main()
