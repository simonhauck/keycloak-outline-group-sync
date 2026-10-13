# Keycloak → Outline Group Sync

A small self-hosted Sync Service that keeps [Outline](https://www.getoutline.com) group membership and team roles in sync with [Keycloak](https://www.keycloak.org), so access to Outline collections is granted through Keycloak and never maintained twice. Every **Client Role** on one configured Keycloak client maps one-to-one to an **Outline Group** whose membership is a Full State Sync of the role's holders; Client Roles named as **Role Signals** drive the Outline **Team Role** instead. Granting access is a pure Keycloak operation; revoking it is too.

## Quick start

Prerequisites: Docker with Compose v2. The service is tested against Keycloak 26.x and Outline 1.10 (the versions pinned by the e2e suite).

1. Create the Client Roles, Role Signals and Keycloak Groups ([Keycloak setup](#keycloak-setup)).
2. Create an Outline API key as a team admin ([Outline setup](#outline-setup)).
3. Copy [`docker-compose.yml`](docker-compose.yml), adjust the values and start it with `DRY_RUN: "true"`.
4. Review the logged plan — every intended group, membership and Team Role change — then set `DRY_RUN: "false"` and restart. The next run applies the same plan.

Pinning a released version instead of `latest` is recommended in production, e.g. `ghcr.io/simonhauck/keycloak-outline-group-sync:0.1.0`.

For external schedulers, `--once` runs a single Sync Run and exits `0` when it succeeded and non-zero when any operation failed:

```sh
docker run --rm \
  -e KEYCLOAK_URL=https://keycloak.example.com \
  -e KEYCLOAK_REALM=my-realm \
  -e KEYCLOAK_CLIENT_ID=outline-sync \
  -e KEYCLOAK_CLIENT_SECRET=... \
  -e OUTLINE_URL=https://outline.example.com \
  -e OUTLINE_TOKEN=... \
  ghcr.io/simonhauck/keycloak-outline-group-sync:0.1.0 --once
```

The long-running default performs a Sync Run at startup, then one every `SYNC_INTERVAL`. Runs never overlap; a failed run is logged and the next one retries. `SIGTERM` cancels cleanly.

## Configuration

All configuration comes from environment variables; there is no config file.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `KEYCLOAK_URL` | yes | – | Base URL of Keycloak, e.g. `https://keycloak.example.com` |
| `KEYCLOAK_REALM` | yes | – | Realm that owns the Client Roles |
| `KEYCLOAK_CLIENT_ID` | yes | – | Confidential client used for the client-credentials grant |
| `KEYCLOAK_CLIENT_SECRET` | yes | – | Secret of that client |
| `KEYCLOAK_ROLES_CLIENT_ID` | no | `KEYCLOAK_CLIENT_ID` | Client whose Client Roles map to Outline Groups |
| `TEAM_ROLE_ADMIN` | no | – | Comma-separated Client Role names whose holders become Outline team admins |
| `TEAM_ROLE_MEMBER` | no | – | Comma-separated Client Role names whose holders become Outline team members |
| `TEAM_ROLE_VIEWER` | no | – | Comma-separated Client Role names whose holders become Outline team viewers |
| `TEAM_ROLE_GUEST` | no | – | Comma-separated Client Role names whose holders become Outline guests |
| `TEAM_ROLE_FALLBACK` | no | `viewer` | Team Role for Managed Accounts without a Role Signal: `guest`, `viewer` or `member` |
| `OUTLINE_URL` | yes | – | Base URL of Outline, e.g. `https://outline.example.com` |
| `OUTLINE_TOKEN` | yes | – | API key created by a team admin |
| `SYNC_INTERVAL` | no | `5m` | Time between Sync Runs; a positive Go duration such as `30s`, `5m`, `1h` |
| `DRY_RUN` | no | `false` | Log the plan without writing to Outline |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |

Missing or invalid settings fail fast at startup with an error naming the setting. `KEYCLOAK_URL` and `OUTLINE_URL` must use `http` or `https`. A `TEAM_ROLE_*` role name that does not exist on the roles client aborts the Sync Run before any write, and so does a name listed under two Team Roles.

## How it works

- The Sync Service polls Keycloak and reads every Client Role of the roles client, plus each user's effective client role mappings — direct assignments and roles inherited through Keycloak Groups and their ancestors.
- Each group-mapping Client Role becomes a **Managed Group** named exactly after the role, tagged `keycloak:<realm>:<roles-client-id>:<role-uuid>`. Roles with no holders still get their group. An existing unmanaged group with a matching name is adopted rather than duplicated; groups carrying another tool's external id are left alone.
- Role Holders are matched to existing Outline Accounts by lowercased exact email; missing members are added and extra members removed. An account whose email is shared by several enabled Keycloak users is never modified.
- Role Signals never become groups. Every **Managed Account** gets the highest signal it holds — Admin > Member > Viewer > Guest — or the Fallback Team Role (`Viewer` by default). The token owner's account is skipped because Outline forbids changing your own role.
- A run reads the complete state before writing anything; a failed read aborts it before Outline is touched. Transient failures and rate limits are handled by the next Sync Run, so an initial sync of a large client may take a few intervals. See [ADR-0005](docs/adr/0005-next-run-is-the-retry.md).

## Guarantees and boundaries

- **Membership and Team Roles only.** The service never creates, invites, suspends or deletes Outline Accounts, and never changes collection permissions.
- **Keycloak is the source of truth.** Manual edits to Managed Groups and Team Roles in Outline are overwritten on the next Sync Run; accounts with no Keycloak counterpart are never touched.
- **Unmanaged Outline Groups are never touched**, including their membership.
- **Orphaned Managed Groups are preserved.** If a Client Role is deleted or becomes a Role Signal, its group, members and access grants stay as they are and the group is reported as orphaned on each run.
- **No writes to Keycloak.** The sync is one-way.
- **Users arrive through SSO:** a Role Holder gains membership, and a Role Signal gains a Team Role, only after the first SSO login creates the Outline Account plus one Sync Run.
- **Disabled users are excluded,** so they lose their Managed Groups and fall back to the Fallback Team Role on the next run. Ambiguously matched accounts are left alone.

The full vocabulary and the decisions behind these rules live in [CONTEXT.md](CONTEXT.md) and [`docs/adr/`](docs/adr).

## Keycloak setup

### 1. Client Roles

Choose the client whose Client Roles define your Outline Groups — the same client Outline uses for SSO, or a dedicated one. Create one Client Role per Outline Group (for example `team-engineering`, `team-design`). If it differs from the sync service account's client, set `KEYCLOAK_ROLES_CLIENT_ID`.

### 2. Role Signals (optional)

To let Keycloak control Outline's team roles, name one or more Client Roles per Team Role in the `TEAM_ROLE_*` variables — for example `TEAM_ROLE_ADMIN=outline-admin`. A Role Signal never becomes an Outline Group; it only sets the holder's Team Role, and the highest match wins: Admin > Member > Viewer > Guest. Role names are matched exactly and may be listed once, under one Team Role. Signals can be granted directly or bundled through Keycloak Groups like any other Client Role.

### 3. Keycloak Groups

Create Keycloak Groups that bundle the roles, map the Client Roles to them and add users. Role mappings are inherited from parent groups to child groups, so hierarchies work as expected. Users can also receive a role by direct assignment. Disabled users, users without an email and users sharing an email are skipped, with a warning.

### 4. Sync service account

Create a confidential client with **Service accounts enabled** (client credentials) and copy its secret into `KEYCLOAK_CLIENT_SECRET`. On the service account's **Service account roles**, assign from the `realm-management` client:

- `view-users` — list users and read their role mappings
- `view-clients` — resolve the roles client and list its Client Roles

`e2e/provision.sh` is a complete, working example of the realm, roles, groups and service account setup, driven by `kcadm.sh`.

## Outline setup

The service authenticates with an Outline API key. On a fresh SSO-only instance, complete one SSO login first: Outline creates the team and makes that first user a team admin. If you bootstrapped the instance with `installation.create`, logging in through SSO with the admin's email claims the existing admin account. Then open **Settings → API Tokens**, create a token and copy it into `OUTLINE_TOKEN`.

Notes:

- API keys are bearer tokens and do not depend on how their owner logs in, so this works with an SSO-only Outline.
- The key must be able to read user emails, which is how accounts are matched. Team admins always can; masked emails cause the affected users to be skipped with a warning.
- The service never creates accounts, so users must have logged in to Outline through SSO at least once.

## Troubleshooting

- **A user is not added.** Look for a `skipping Keycloak user` warning: the user may be disabled, have no email, share an email with another user, or have no Outline Account yet.
- **Everyone became a Viewer.** No `TEAM_ROLE_*` signal is configured, so every Managed Account falls back to `TEAM_ROLE_FALLBACK`. Configure the signals, or change the fallback.
- **Team Role writes fail with an authorization error.** Outline versions that gate guest access reject `Guest` without the Guests entitlement; Community self-hosted instances should use `TEAM_ROLE_FALLBACK=viewer` (or `member`).
- **An orphaned group warning.** The Client Role behind that group no longer exists or became a Role Signal. The group and its access grants are left untouched; delete it, or unset the signal, to resolve it.
- **The first sync takes several runs.** Outline rate-limits its API (by default 1000 requests/60s; `groups.create` at 10/min). Each run makes as much progress as it can; self-hosters with a very large first sync can raise Outline's `RATE_LIMITER_MULTIPLIER` or `RATE_LIMITER_REQUESTS`.
- **No writes happen at all.** A read failed — in Keycloak or in Outline — so the run aborted before writing anything. The error is in the logs.

To roll back: stop the container. Nothing is deleted and the last applied state stays in place. To undo a membership or Team Role change, adjust Keycloak and run again, or edit Outline — but managed state is reconciled on every run, so manual edits are temporary.

## Development

```sh
go vet ./...
go test ./...
```

The end-to-end suite runs the service image against real Keycloak and Outline in Docker Compose, including the scripted SSO login:

```sh
go test -tags e2e -count=1 -v ./e2e/
```

Container-image checks (non-root, no toolchain, `--once` against fakes) are behind a tag:

```sh
go test -tags docker ./internal/app/
```

See [`e2e/README.md`](e2e/README.md) for requirements and details.

## Releases

Releases are automated with [release-please](https://github.com/googleapis/release-please) from conventional squash-merge commit messages, so PR titles must be conventional (a CI check enforces it). Merging the release PR tags `vX.Y.Z`, updates [`CHANGELOG.md`](CHANGELOG.md) and publishes the image to GHCR tagged `X.Y.Z`, `X.Y` and `latest` (`latest` only for stable releases).

Research notes behind the API usage live in [`docs/research/`](docs/research).

## License

[MIT](LICENSE)
