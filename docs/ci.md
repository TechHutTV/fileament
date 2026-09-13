# Validation and build dependencies

PRs, branch pushes, manual CI runs, and the weekly Monday 08:23 UTC schedule run `.github/workflows/ci.yml`, which also exposes the same validation as a reusable workflow for release tags. Scheduled runs check the default branch, so they detect newly published Go/npm advisories even when application code has not changed. GitHub starts the schedule after the workflow reaches the default branch; it can delay scheduled jobs.

The shared workflow runs release-policy tests, workflow lint, all first-party Go tests and vet, the full Go race suite, bounded mesh fuzzing, Go/npm vulnerability scans, frontend checks, and a production image build/runtime smoke. The normal Go tests use `CGO_ENABLED=0`; only the race suite enables CGO for the race detector. The hosted Linux toolchain comes from `go.mod`, and a validation gate requires the pinned Docker builder to use the same Go patch version. Node stays on release line 22. Validation has a 30-minute job timeout.

Each fuzz target runs for 15 seconds with two workers, a one-second minimization budget, and a two-minute command timeout. STL covers text and binary seeds; OBJ covers ordinary and relative indices; 3MF covers both archive bytes and XML repackaged into a valid archive. Inputs are capped at 128 KiB, with smaller geometry/archive limits and a one-second parser context. Accepted geometry must have finite bounds and coordinates, match streaming inspection, and preserve the input's checksum. These bounded runs supplement adversarial tests; they are not exhaustive parser verification. To investigate a failure, run its saved corpus entry with the reported `go test -run` command, then keep a minimal regression fixture with the fix.

All validation jobs have only `contents: read`. Checkout does not persist its token, and dependency caches are disabled. The final step checks the checkout revision and tracked-file diff, then returns the validated commit. Release tags call this same workflow; only a successful result allows the separate publishing job, which checks out that exact commit. Before registry login, the publishing job requires the current remote tag to resolve to the validated commit, including annotated tags. A moved/deleted tag or a different checkout fails the release. Only the publishing job receives `contents: write` and `packages: write`.

Ordinary CI loads its test image locally and never pushes it. The Buildx, metadata, and build actions are exercised there; GHCR login, multi-architecture publication, attestations, and GitHub release creation remain release-only operations. Changes to release automation do not authorize creating a real tag. After an explicitly approved release, verify the workflow, exact GitHub release, GHCR manifest, both architectures, and intended `latest` tag as described in `AGENTS.md`.

## Updating pins

Third-party actions use full commit SHAs with release-version comments. Dockerfile base images and the smoke test's ownership helper use multi-architecture index digests. These pins prevent an upstream tag change from silently replacing those inputs; they do not make network-dependent builds hermetic.

Dependabot proposes weekly grouped updates for GitHub Actions and Dockerfile images. Updates remain review PRs and are not automatically merged. For a manual action update, resolve the release tag in the official action repository and review its release notes, runtime requirements, and changed inputs:

```sh
gh api repos/actions/checkout/commits/v7.0.1 --jq .sha
```

Update the SHA and version comment together in every workflow that uses the action. Prefer maintained Node 24 actions, and keep checkout credentials disabled. The updated workflow must pass actionlint, release-policy tests, and the same validation/build/smoke path before merge.

For a container update, use the registry's multi-architecture index digest, rather than an individual platform's image digest. Verify the pinned reference resolves and includes both required platforms:

```sh
docker manifest inspect gcr.io/distroless/static-debian13:nonroot
```

Review the upstream image changes and the digest in the update PR. Keep `go.mod`, the Go builder tag, and the documented Go commands on the same supported patch version. `setup-go` reads `go.mod`; it does not select a floating Go release. Retain the Node 22 line unless its compatibility change has been reviewed separately. The literal `PERMISSIONS_IMAGE` in `.github/scripts/smoke_container.py` is outside Dependabot's Dockerfile discovery: review and refresh its digest with container updates, and keep the README ownership-migration command's version aligned. Confirm the resulting image runs under both the default and a custom non-root user through the production smoke.

`govulncheck` and `actionlint` are versioned Go tools resolved through Go module verification. Review their pinned versions when maintaining workflows. No workflow needs publication credentials to lint, test, scan, or build a local image.
