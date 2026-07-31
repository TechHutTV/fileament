# Fileament release-note policy

Fileament uses two release-note formats based on Semantic Versioning:

- `vX.Y.0` minor and major releases require curated, detailed notes.
- `vX.Y.Z` patch releases where `Z > 0` use concise GitHub-generated notes.

## Detailed releases (`vX.Y.0`)

Before creating the tag, copy `TEMPLATE.md` to a versioned file such as `v1.3.0.md`. Complete every placeholder and commit that file with the release changes.

Detailed notes must include, in order:

1. A release summary and early-development warning.
2. `## Highlights` with user-facing explanations.
3. `## Upgrade notes` covering data, schema, configuration, and backup implications.
4. The exact versioned GHCR pull command.
5. `## Changes by pull request`, using exact PR titles and PR-level bullets.
6. A full changelog comparison ending at the release tag.

Validate the file before tagging:

```sh
python3 .github/scripts/prepare_release_notes.py \
  --tag v1.3.0 \
  --image ghcr.io/TechHutTV/fileament \
  --repository TechHutTV/fileament \
  --validate-only
```

The release workflow repeats this validation before building or publishing. A missing, incomplete, or unfinished versioned file fails the workflow.

## Patch releases (`vX.Y.Z`, `Z > 0`)

No versioned Markdown file is required. The release workflow produces a concise release containing:

1. The release title and a one-line patch summary.
2. The exact versioned GHCR pull command and published architectures.
3. `## Fixes and improvements`, generated from merged pull requests.
4. GitHub's full changelog comparison.

Preview GitHub's generated PR range before tagging and confirm it starts at the intended previous release.
