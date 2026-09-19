# Keycloak → Outline Group Sync

A small self-hosted Sync Service that keeps [Outline](https://www.getoutline.com) group membership in sync with [Keycloak](https://www.keycloak.org), so access to Outline collections is granted through Keycloak and never maintained twice.

Every **Client Role** on one configured Keycloak client maps one-to-one to an **Outline Group**, and the group's membership is a **Full State Sync** of the role's holders. Granting access is a pure Keycloak operation; revoking it is too.

## How it works

- The Sync Service polls Keycloak on a schedule and reads every Client Role of the configured client, plus each user's effective client role mappings — direct assignments and roles inherited through Keycloak Groups and their ancestors.
- Each Client Role maps to an Outline Group named exactly after the role, tagged with a stable external id: `keycloak:<realm>:<roles-client-id>:<role-uuid>`. Roles with no holders still get their group.
- An existing unmanaged group whose name matches a Client Role is adopted (and tagged) rather than duplicated, so you can migrate without recreating access grants. Groups that already carry an external id from another tool are left alone and a new Managed Group is created. Adoption starts reconciling membership immediately, so pre-existing extra members are removed on that first run.
- Role Holders are matched to existing Outline Accounts by lowercased exact email; missing members are added and extra members are removed.
- An Outline Account whose email is shared by several enabled Keycloak users is never modified — neither added nor removed — because the service cannot know which user owns it.
- A run reads the complete state from Keycloak before writing anything. If any read fails, in Keycloak or in Outline, the run aborts before touching Outline.
- Transient failures and Outline rate limits are handled by the next Sync Run; runs are idempotent, so an initial sync of a large client may take a few intervals. See [ADR-0005](docs/adr/0005-next-run-is-the-retry.md).

## Guarantees and boundaries

- **Membership only.** The service never creates, invites, suspends or deletes Outline Accounts, and never changes team roles or collection permissions.
- **Keycloak is the source of truth.** Manual membership edits to Managed Groups in Outline are overwritten on the next Sync Run.
- **Unmanaged Outline Groups are never touched.** Groups that do not match a Client Role are left alone, including their membership.
- **Orphaned Managed Groups are preserved.** If a Client Role is deleted, its group, members and access grants stay as they are and the group is reported as orphaned on each run.
- **No writes to Keycloak.** The sync is one-way.
- **Users arrive through SSO.** A Role Holder gains Outline Group membership only after their first SSO login creates their Outline Account plus one Sync Run.
- **Disabled users are excluded,** so a user disabled in Keycloak is removed from their Managed Groups on the next run.
- **Ambiguous users are left alone.** When several Keycloak users share an email, the Outline Account behind it is neither added nor removed.

The full vocabulary and the decisions behind these rules live in [CONTEXT.md](CONTEXT.md) and [`docs/adr/`](docs/adr).

## Quick start

Prerequisites: Docker with Compose v2. The service is tested against Keycloak 26.x and Outline 1.10 (the versions pinned by the e2e suite).

1. Create the Client Roles, Keycloak Groups and the sync service account ([Keycloak setup](#keycloak-setup)).
2. Create an Outline API key as a team admin ([Outline setup](#outline-setup)).
3. Copy the example [`docker-compose.yml`](docker-compose.yml), adjust the values and start it with `DRY_RUN: "true"`.
4. Watch a run and confirm the plan looks right:

   ```sh
   docker compose up -d
   docker compose logs -f
   ```

5. Set `DRY_RUN: "false"` and restart. The next run applies the same plan.

Pinning a released version instead of `latest` is recommended in production, e.g. `ghcr.io/simonhauck/keycloak-outline-group-sync:0.1.0`.

### One-shot mode

For external schedulers, run a single Sync Run and exit:

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

`--once` exits `0` when the run succeeded and non-zero when any operation failed.

## Keycloak setup

### 1. Client Roles

Choose the client whose Client Roles will define your Outline Groups. It can be the same client Outline uses for SSO, or a dedicated client. Create one Client Role per Outline Group (for example `team-engineering`, `team-design`).

If the roles live on a different client than the sync service account, set `KEYCLOAK_ROLES_CLIENT_ID` to that client's id.

### 2. Keycloak Groups

Create Keycloak Groups that bundle the roles, and map the Client Roles to those groups. Add users to the groups. Role mappings are inherited from parent groups to child groups, so hierarchies work as expected.

Users can also receive a role by direct assignment. Disabled users, users without an email and users sharing an email are skipped, with a warning.

### 3. Sync service account

Create a confidential client for the service with **Service accounts enabled** (client credentials). Copy its secret into `KEYCLOAK_CLIENT_SECRET`.

On the service account's **Service account roles**, assign these roles from the `realm-management` client:

- `view-users` — list users and read their role mappings
- `view-clients` — resolve the roles client and list its Client Roles

`e2e/provision.sh` is a complete, working example of the realm, roles, groups and service account setup, driven by `kcadm.sh`.

## Outline setup

The service authenticates with an Outline API key. Create one while logged in as a **team admin**:

1. Log in to Outline. On a fresh SSO-only instance, complete one SSO login first: Outline creates the team and makes that first user a team admin. If you bootstrapped the instance with `installation.create`, logging in through SSO with the admin's email claims that existing admin account instead of creating a new one.
2. Open **Settings → API Tokens**, create a token and copy it into `OUTLINE_TOKEN`.

Notes:

- API keys are bearer tokens and do not depend on how their owner logs in, so this works with an SSO-only Outline.
- The key must be able to read user emails, which is how Role Holders are matched to accounts. Team admins always can; with Outline's default email visibility, members can too. If emails come back masked, the affected users are skipped with a warning.
- The service never creates accounts. Users must have logged in to Outline through SSO at least once before a Sync Run can add them to a group.
- Roll out with `DRY_RUN=true` first: with writes enabled the service starts converging immediately.

## Configuration

All configuration comes from environment variables; there is no config file.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `KEYCLOAK_URL` | yes | – | Base URL of Keycloak, e.g. `https://keycloak.example.com` |
| `KEYCLOAK_REALM` | yes | – | Realm that owns the Client Roles |
| `KEYCLOAK_CLIENT_ID` | yes | – | Confidential client used for the client-credentials grant |
| `KEYCLOAK_CLIENT_SECRET` | yes | – | Secret of that client |
| `KEYCLOAK_ROLES_CLIENT_ID` | no | `KEYCLOAK_CLIENT_ID` | Client whose Client Roles map to Outline Groups |
| `OUTLINE_URL` | yes | – | Base URL of Outline, e.g. `https://outline.example.com` |
| `OUTLINE_TOKEN` | yes | – | API key created by a team admin |
| `SYNC_INTERVAL` | no | `5m` | Time between Sync Runs; a positive Go duration such as `30s`, `5m`, `1h` |
| `DRY_RUN` | no | `false` | Log the plan without writing to Outline |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |

Missing or invalid settings fail fast at startup with an error naming the setting. `KEYCLOAK_URL` and `OUTLINE_URL` must use `http` or `https`.

## Running modes

- **Long-running (default):** a Sync Run at startup, then one every `SYNC_INTERVAL`. Runs never overlap; a failed run is logged and the next run retries. `SIGTERM` cancels cleanly.
- **`--once`:** a single Sync Run for external schedulers. It exits non-zero when the run failed — invalid configuration, a failed read, or any failed operation — and zero otherwise.

## Dry run and rollback

Start with `DRY_RUN=true` and review the logged plan — every intended group create, adoption and rename and every member addition and removal, with a summary. Runs in dry-run mode write nothing.

To roll back: stop the container. The Sync Service never deletes groups and never manages accounts or permissions, so the last applied state stays in place. To undo a membership change, adjust Keycloak and run again, or edit the group in Outline — but remember that Managed Groups are reconciled on every run, so manual edits are temporary.

## Troubleshooting

- **A user is not added.** Look for a `skipping Keycloak user` warning: the user may be disabled, have no email, share an email with another user, or have no Outline Account yet (they need one SSO login first).
- **An orphaned group warning.** The Client Role behind that group no longer exists. The group and its access grants are left untouched; delete the role only if you intend that, or recreate it to resume syncing.
- **The first sync takes several runs.** Outline rate-limits its API (by default 1000 requests/60s; `groups.create` at 10/min). Each run makes as much progress as it can and the next one continues. Self-hosters with a very large first sync can raise Outline's `RATE_LIMITER_MULTIPLIER` or `RATE_LIMITER_REQUESTS`.
- **No writes happen at all.** A read failed — in Keycloak or in Outline — so the run aborted before writing anything. The error is in the logs.

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
