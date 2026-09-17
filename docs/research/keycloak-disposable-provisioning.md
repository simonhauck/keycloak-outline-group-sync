# Disposable Keycloak 26.x provisioning for docker-compose CI

Verified 2026-09-15 by running `quay.io/keycloak/keycloak:26.7.3` (digest `sha256:29be7252db0a106f1cd2ac17b9a56ff2668073da645638a38b9fc67deeb2d6c4`) with Docker Compose v5.5.1. Live-verified items are marked (live). Sources at the end.

## 1. kcadm.sh in the official image

- Path: `/opt/keycloak/bin/kcadm.sh` (distro tarball `bin/kcadm.sh` unpacked to `/opt/keycloak`; image entrypoint is `/opt/keycloak/bin/kc.sh`, no HEALTHCHECK, runs as uid 1000). (live)
- Auth: `kcadm.sh config credentials --server http://keycloak:8080 --realm master --user admin --password ...`; service account: `--client <id> --secret <secret>`; env `KC_CLI_PASSWORD` / `KC_CLI_CLIENT_SECRET`; session file defaults to `/opt/keycloak/.keycloak/kcadm.config` (`HOME=/opt/keycloak`, writable). (live)
- Shapes:
  - `create realms -s realm=$R -s enabled=true -s duplicateEmailsAllowed=true`
  - `CID=$(create clients -r $R -s clientId=role-client -s enabled=true -s publicClient=false -s clientAuthenticatorType=client-secret -s secret=role-secret -i)`
  - `create clients/$CID/roles -r $R -s name=team-a`
  - sync client: `-s serviceAccountsEnabled=true ...` then `add-roles -r $R --uusername service-account-sync --cclientid realm-management --rolename view-users --rolename view-clients` (live)
  - group: `GID=$(create groups -r $R -s name=g -i)`; `add-roles -r $R --gid $GID --cclientid role-client --rolename team-a`; membership `update users/$UID/groups/$GID -r $R -n` (live)
  - users: `create users -s username=... -s enabled=true -s firstName=... -s lastName=... -s email=...`; password `set-password -r $R --username u --new-password p` (omit `--temporary`)
- One-shot vs `exec`: both work; prefer one-shot. Verified compose: healthcheck + `depends_on: {condition: service_healthy}` + `docker compose run --rm provisioner` exits with the script's code (0) and can create the realm (live). `exec` works too but is ordered manually. Caveat: all commands must run in one container invocation only if you rely on the session file; otherwise use `--no-config` per command.

## 2. Admin bootstrap

- `KC_BOOTSTRAP_ADMIN_USERNAME` / `KC_BOOTSTRAP_ADMIN_PASSWORD` (live). Master realm admin is created only on first start when master does not exist.
- `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` still work in 26.7.3 but log `KC-SERVICES0110: ... is deprecated` (live); docs deprecate them since 26.0. Use the new names.
- `start-dev` for tests: no hostname/TLS/DB setup, H2 dev-file DB. Health ready in ~15 s (live). `start` requires hostname and TLS/DB config; unnecessary here.

## 3. Readiness

- `KC_HEALTH_ENABLED=true` exposes health on management port 9000: `/health/ready`, `/health/live`; port 8080 returns 404 (live).
- Documented HEALTHCHECK snippet works inside the image (bash + grep present) (live):
  `bash -c "{ printf 'HEAD /health/ready HTTP/1.0\r\n\r\n' >&0; grep 'HTTP/1.0 200'; } 0<>/dev/tcp/localhost/9000"`

## 4. Realm import alternative

- Mount `/opt/keycloak/data/import`, file must be `<realm>-realm.json`, run `start-dev --import-realm`; existing realms are skipped (log: `Strategy: IGNORE_EXISTING`). (live)
- Users support plaintext `credentials: [{type: password, value: ..., temporary: false}]`; the value is hashed on import (argon2) and login works (live).
- `temporary: true` is silently ignored on import in 26.7.3: the user can log in and `requiredActions` is empty (`RepresentationToModel.createCredentials` never reads `isTemporary`). (live; may change in later releases)
- Import is a snapshot of the admin API model with all-or-nothing realm semantics; kcadm fails loudly per command. For a test fixture, kcadm is more debuggable; import has fewer moving parts but drifts silently.

## 5. 26.x gotchas

- FGAP v2 is a `Type.DEFAULT` feature in 26.7.3 (v1 deprecated), but opt-in per realm via `adminPermissionsEnabled`; classic `realm-management` roles still work. Live: sync client with only `view-users` + `view-clients` got 200 on `GET /admin/realms/{r}/users` and `/clients`.
- Role-mapping/admin paths take the client UUID; `-i` captures it, `add-roles` accepts `--cclientid`.
- `serviceAccountsEnabled=true` + confidential creates `service-account-<clientId>`.
- `directAccessGrantsEnabled` defaults to **false** (live) — set `-s directAccessGrantsEnabled=true` only for password-grant tests; not needed for `client_credentials`.
- Default user profile requires `email`, `firstName`, `lastName` for role `user`; without them password login fails with `resolve_required_actions / Account is not fully set up` (live). Set them on any user expected to log in.
- `enabled` defaults to false when omitted (live) — always pass `-s enabled=true`.
- Client secret is auto-generated for confidential clients; set `-s secret=...` for deterministic tests.
- `duplicateEmailsAllowed` is a realm attribute, default false.

## 6. Version

- Latest stable 26.x: **26.7.3** (quay tags; docs build labelled 26.7.3). Pin by tag or digest above.

## Recommended provisioning recipe: kcadm.sh one-shot

```yaml
services:
  keycloak:
    image: quay.io/keycloak/keycloak:26.7.3
    command: start-dev
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: admin
      KC_BOOTSTRAP_ADMIN_PASSWORD: admin
      KC_HEALTH_ENABLED: "true"
    healthcheck:
      test: ["CMD", "bash", "-c", "{ printf 'HEAD /health/ready HTTP/1.0\r\n\r\n' >&0; grep 'HTTP/1.0 200'; } 0<>/dev/tcp/localhost/9000"]
      interval: 2s
      timeout: 3s
      retries: 60
  provisioner:
    image: quay.io/keycloak/keycloak:26.7.3
    depends_on:
      keycloak: { condition: service_healthy }
    entrypoint: ["bash", "/provision.sh"]   # kcadm.sh + mounted script
    volumes: ["./provision.sh:/provision.sh:ro"]
    restart: "no"
```

CI: `docker compose up -d --wait keycloak && docker compose run --rm provisioner` (verified, exit 0). In the script, `set -euo pipefail`, then `kcadm.sh config credentials ...`, create realm/client roles/sync client/realm-management roles/group/mappings/users/passwords as in section 1. Prefer this over `--import-realm` for test fixtures; use a committed `<realm>-realm.json` only if you accept silent field-ignoring and set `firstName`/`lastName`/`email` on every login-capable user.

Flagged unverified: `depends_on: service_healthy` semantics for `docker compose run` may vary by Compose version (verified with v5.5.1); `KC_HEALTH_ENABLED` is a build-time option applied by the implicit rebuild in `start`/`start-dev`.

## Sources

1. Bootstrap admin guide — https://www.keycloak.org/server/bootstrap-admin-recovery
2. Containers guide (bootstrap env, import dir) — https://www.keycloak.org/server/containers
3. Health guide — https://www.keycloak.org/observability/health
4. Import/export guide — https://www.keycloak.org/server/importExport
5. Admin CLI — https://github.com/keycloak/keycloak/blob/26.7.3/docs/documentation/server_admin/topics/admin-cli.adoc
6. `kcadm.sh` — https://github.com/keycloak/keycloak/blob/26.7.3/integration/client-cli/admin-cli/src/main/bin/kcadm.sh
7. `RepresentationToModel.createCredentials` — https://github.com/keycloak/keycloak/blob/26.7.3/server-spi-private/src/main/java/org/keycloak/models/utils/RepresentationToModel.java
8. `UserCredentialModel.password(value, adminRequest)` — https://github.com/keycloak/keycloak/blob/26.7.3/server-spi/src/main/java/org/keycloak/models/UserCredentialModel.java
9. `Profile.java` (FGAP v1 deprecated / v2 default) — https://github.com/keycloak/keycloak/blob/26.7.3/common/src/main/java/org/keycloak/common/Profile.java
10. Fine-grained admin permissions (classic roles still valid) — https://github.com/keycloak/keycloak/blob/26.7.3/docs/documentation/server_admin/topics/admin-console-permissions/fine-grain-v2.adoc
11. 26.0 upgrading changes (`KEYCLOAK_ADMIN` deprecation) — https://github.com/keycloak/keycloak/blob/26.7.3/docs/documentation/upgrading/topics/changes/changes-26_0_0.adoc
12. Image Containerfile — https://github.com/keycloak/keycloak/blob/26.7.3/quarkus/container/Dockerfile
13. Quay tags — https://quay.io/repository/keycloak/keycloak?tab=tags
