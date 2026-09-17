# Headless Outline admin + API-key bootstrap (v1.10.0)

Verified 2026-09-15 against `outlinewiki/outline:1.10.0` (digest `sha256:cc9f1f05fd7cd316745b5fc9cfe01e681b1b4cf97705d1d812a5c778d7986cfd`, label `revision=65be53c3eb093a535ce260eb2f2da897bb07a29e` = git tag `v1.10.0`), run live with Postgres 16 + Redis 7. All source links below are pinned to that commit. Sources at the end.

## 1. API key model

- `server/models/ApiKey.ts`: prefix `ol_api_`; new tokens are `ol_api_` + `randomString(38)` (mixed alphanumeric). The token itself is not stored: `hash` holds `sha256hex(token)` (unique), `secret` is deprecated + nullable, `last4` is display-only, `scope=null` means full access, `expiresAt` nullable.
- `server/utils/crypto.ts`: `hash()` is plain SHA-256 hex. `ApiKey.findByToken` accepts `secret` OR `hash`; `ApiKey.match()` gates the lookup on exactly 38 `\w` chars after the prefix.
- Verified live: `printf <token> | sha256sum` equals the `hash` column; a raw SQL row with only `id, name, hash, last4, "userId", "createdAt", "updatedAt"` authenticates. A 34-char suffix returned `Unable to decode token`, confirming the 38-char gate.

## 2. CLI / seeding options

- `server/scripts/`: `bootstrap.ts`, `checkMigrations.ts`, data backfills (incl. `20240930113921-hash-api-keys.ts`), `reset-encrypted-data.ts`. Nothing creates users/keys/teams. `Makefile`/`package.json` `db:migrate` only. `server/test/factories.ts::buildApiKey` is vitest-only.
- Outline CI (`.github/workflows/ci.yml`) runs vitest against a Postgres service; no Cypress/e2e exists in v1.10.0.
- Image layout: `/opt/outline/{build,server,node_modules,public,package.json}`, WORKDIR `/opt/outline`. SWC rewrote `@server/*` imports to relative requires, so a `node -e` inside the container can `require("/opt/outline/build/server/storage/database")` then `/opt/outline/build/server/models` and call `Team/User/ApiKey.create` (verified; `key.value` is the plaintext). Requiring the DB module first is mandatory or models are uninitialized.
- Best route: `POST /api/installation.create` (self-hosted only, no auth, only when `Team.count()===0`) creates team + Admin user and signs in, setting an `accessToken` JWT cookie. Then `POST /api/apiKeys.create` with that JWT as Bearer returns `data.value`. Fully verified live.

## 3. First-user admin

`server/commands/accountProvisioner.ts:190`: `role: isNewTeam ? UserRole.Admin : undefined`; `teamProvisioner.ts` creates the team on the first unknown SSO login. `installation.ts:42` sets `role: UserRole.Admin` explicitly.

## 4. Headless OIDC

Routes `GET /auth/oidc` and `GET|POST /auth/oidc.callback` (`plugins/oidc/server/auth/oidcRouter.ts:277-284`). State is HMAC-signed; the nonce lives in an HttpOnly `oauth_csrf` cookie (10 min) and must match at callback (`server/utils/passport.ts:185,218`). PKCE is on only if discovery advertises S256 (Keycloak does). Success sets an `accessToken` cookie (`server/utils/authentication.ts:150`). CSRF (`server/middlewares/csrf.ts`) is skipped for non-cookie transports; cookie calls need `x-csrf-token` plus matching cookie. A curl cookie-jar script could do this, but it must parse Keycloak's login form/CSRF and drive PKCE. Untested here; brittle. Prefer §2.

## 5. Health / minimal services

- `GET /_health` returns `OK`; it pings Postgres and Redis (`server/main.ts:78`). `/api/health` → 404. Image HEALTHCHECK uses `/_health`.
- Redis is required: without `REDIS_URL` startup fails with `Environment configuration is invalid ... REDIS_URL should not be empty` (verified). Minimal set: Outline + Postgres + Redis.

## 6. Env

Required: `NODE_ENV=production`, `SECRET_KEY`, `UTILS_SECRET`, `DATABASE_URL`, `REDIS_URL`, `URL`, `PGSSLMODE=disable`, and **`FORCE_HTTPS=false`** (`server/env.ts:365` defaults true in production and 301-redirects plain HTTP; verified). OIDC: `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_ISSUER_URL` (or manual `OIDC_AUTH_URI`/`OIDC_TOKEN_URI`/`OIDC_USERINFO_URI`), `OIDC_DISPLAY_NAME`, `OIDC_USERNAME_CLAIM`, `OIDC_SCOPES`.

## 7. Community image

`Dockerfile` entrypoint `docker-entrypoint.sh`, CMD `node build/server/index.js`; HEALTHCHECK `wget /_health`; runs migrations automatically on start (`checkMigrations.ts`; observed "Migrating ..." in logs); non-root uid 1001; migrations loaded from `/opt/outline/server/migrations` via `cwd: path.resolve("server")`.

## Recommended bootstrap (ranked)

1. HTTP (verified): `POST /api/installation.create` (fresh empty DB only), capture `accessToken` cookie, then `POST /api/apiKeys.create` with `Authorization: Bearer <cookie>`, read `data.value`. No DB/browser/SSO.
2. In-container Node script (verified): `node -e` requiring DB + models, creating team/admin/ApiKey. Works after any startup; idempotency is your job.
3. Raw SQL (verified): insert `apiKeys` row with sha256 hash of a valid 38-char token.
4. Scripted OIDC (unverified, avoid).

## Sources

- https://github.com/outline/outline/blob/v1.10.0/server/models/ApiKey.ts
- https://github.com/outline/outline/blob/v1.10.0/server/utils/crypto.ts
- https://github.com/outline/outline/blob/v1.10.0/server/migrations/20240929194201-add-hash-to-api-key.js
- https://github.com/outline/outline/blob/v1.10.0/server/routes/api/installation/installation.ts
- https://github.com/outline/outline/blob/v1.10.0/server/commands/accountProvisioner.ts
- https://github.com/outline/outline/blob/v1.10.0/server/commands/teamProvisioner.ts
- https://github.com/outline/outline/blob/v1.10.0/plugins/oidc/server/auth/oidcRouter.ts
- https://github.com/outline/outline/blob/v1.10.0/server/utils/passport.ts
- https://github.com/outline/outline/blob/v1.10.0/server/middlewares/csrf.ts
- https://github.com/outline/outline/blob/v1.10.0/server/main.ts
- https://github.com/outline/outline/blob/v1.10.0/server/env.ts
- https://github.com/outline/outline/blob/v1.10.0/server/scripts/checkMigrations.ts
- https://github.com/outline/outline/blob/v1.10.0/Dockerfile
- https://github.com/outline/outline/blob/v1.10.0/.env.sample
- https://docs.getoutline.com/s/hosting/doc/redis-LGM4BFXYp4
- https://docs.getoutline.com/s/hosting/doc/oidc-8CPBm6uC0I
