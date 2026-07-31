# Fileament v{{VERSION}}

{{ONE_PARAGRAPH_RELEASE_SUMMARY}}

> [!WARNING]
> Fileament is still in the early stages of development. Back up your `/data` volume and review the release notes before updating.

## Highlights

### {{HIGHLIGHT_TITLE}}

{{USER_FACING_EXPLANATION}}

- {{KEY_CHANGE}}
- {{KEY_CHANGE}}

## Upgrade notes

{{SCHEMA_CONFIGURATION_AND_DATA_COMPATIBILITY_GUIDANCE}}

{{BACKUP_OR_MIGRATION_GUIDANCE}}

```sh
docker pull ghcr.io/techhuttv/fileament:{{VERSION}}
```

If you use Docker Compose, update the image tag and recreate the service:

```sh
docker compose pull
docker compose up -d
```

## Changes by pull request

<!-- Preserve each GitHub PR title exactly, including prefixes and casing. Do not prepend a visible PR number. -->

### [{{EXACT_PR_TITLE}}]({{PR_URL}}) by [@{{AUTHOR}}](https://github.com/{{AUTHOR}})

- {{PR_LEVEL_CHANGE}}
- {{PR_LEVEL_CHANGE}}

**Full changelog:** https://github.com/TechHutTV/fileament/compare/{{PREVIOUS_TAG}}...v{{VERSION}}
