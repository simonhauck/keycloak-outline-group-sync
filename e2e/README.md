# End-to-end suite

Runs the Sync Service against real dependencies in Docker Compose:

- Keycloak 26.7.3, provisioned by a one-shot `kcadm.sh` container (`provision.sh`): realm `e2e`, an OIDC client for Outline, the `roles-client` carrying the Client Roles, a `sync-client` service account with `view-users` and `view-clients`, Keycloak Groups with role mappings, and users covering the direct-role, group-inherited, disabled, duplicate-email and no-account cases.
- Outline 1.10 with Postgres 16 and Redis 7, bootstrapped over the admin API (`installation.create` + `apiKeys.create`) with accounts and groups seeded as fixtures.
- The service image built from the repository `Dockerfile` and run with `--once` for each scenario.

## Running

```sh
go test -tags e2e -count=1 -v ./e2e/
```

If a scenario fails, the compose logs are printed with the failure. The test owns the stack lifecycle and removes its volumes when it finishes, so every run starts clean.

The first run pulls the Keycloak, Outline, Postgres and Redis images and takes a couple of minutes; later runs take about a minute.

## Scenarios

The suite asserts, against real HTTP APIs:

- a Client Role becomes a Managed Group; a pre-existing group is adopted by name; the group id survives adoption
- Keycloak Group membership and direct role assignment both add members
- removing the Keycloak Group membership removes the Outline member; disabling a user removes them
- a user disabled before the first Sync Run is excluded
- renaming a Client Role renames its Managed Group without losing its external id
- duplicate-email users and users without an Outline Account are skipped
- an unmanaged Outline Group is never touched
- a second run is a no-op (unchanged state, zeroed run summary)
- an unreachable Keycloak and an unknown roles client both abort the run before any Outline write
- a headless scripted SSO login (no browser) completes against Outline; the first login of an uninvited Keycloak user JIT-creates the Outline Account, and the next Sync Run adds it to its Managed Group
- a Keycloak user matching the installation admin's email claims that admin account through SSO, and an API key minted from that session is accepted by the service — the documented production bootstrap

The SSO login is pure HTTP: it starts at Outline's `/auth/oidc`, parses Keycloak's rendered login form, posts the credentials, and follows the callback. Connections to Keycloak's internal name are dialed at the published loopback port while the host header stays `keycloak:8080`, so Keycloak's token issuer matches what Outline expects on the server side.

## Requirements

- Docker with Compose v2
- A Linux host: the stack publishes Keycloak on loopback port `18080` and Outline on loopback port `13000` for the test client.

Accounts used by the membership scenarios are seeded through the admin API; the SSO scenarios above exercise the login path that creates accounts in production.
