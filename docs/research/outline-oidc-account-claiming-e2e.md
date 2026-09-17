# Outline v1.10.0 OIDC account-claiming e2e with Keycloak 26.x

Verified 2026-09-15 by reading source at tag `v1.10.0` (commit `65be53c3eb093a535ce260eb2f2da897bb07a29e`, published 2026-09-02) and Keycloak `26.7.3`. **Source/docs-only — no live run was performed for this note.** Items not confirmed by execution are labelled; see also `keycloak-disposable-provisioning.md` (live-verified Keycloak setup) and `outline-admin-bootstrap.md` (live-verified Outline bring-up).

## 1. Outline OIDC env (v1.10.0)

Plugin is `plugins/oidc` (`plugins/oidc/plugin.json`: `id: "oidc"`). Env schema: `plugins/oidc/server/env.ts`.

| Variable | Default / notes | Source |
| --- | --- | --- |
| `OIDC_CLIENT_ID` | required; mutually dependent with secret | env.ts:14 |
| `OIDC_CLIENT_SECRET` | required | env.ts:18 |
| `OIDC_ISSUER_URL` | enables discovery (`{issuer}/.well-known/openid-configuration`) | env.ts:32, oidcDiscovery.ts:24-57 |
| `OIDC_AUTH_URI` / `OIDC_TOKEN_URI` / `OIDC_USERINFO_URI` | manual alternative; all 3 + client id/secret required together; if set, discovery is skipped | oidc.ts:12-35 |
| `OIDC_DISPLAY_NAME` | "OpenID Connect" | env.ts:40 |
| `OIDC_USERNAME_CLAIM` | `preferred_username`; lodash-style path into profile or decoded id_token | env.ts:78, oidcRouter.ts:199-201 |
| `OIDC_SCOPES` | `openid profile email` | env.ts:85 |
| `OIDC_DISABLE_REDIRECT` | disables auto-redirect on `/login` | env.ts:94 |
| `OIDC_LOGOUT_URI` | only used with manual config; discovery uses `end_session_endpoint` | env.ts:107, oidc.ts:54 |

- Redirect URI is exactly `${env.URL}/auth/${config.id}.callback` → **`{URL}/auth/oidc.callback`** (`oidcRouter.ts:63`; routes at 277-284). Register it verbatim in Keycloak (Keycloak matches exact URIs).
- `URL` must be the "fully qualified, publicly accessible URL" the browser uses (`.env.sample:20-22`); it is also the callback base and JWT/team URL (`server/env.ts:231-241`). `FORCE_HTTPS` defaults true under production `NODE_ENV` and 301-redirects plain HTTP (see `outline-admin-bootstrap.md`).
- **Discovery is fetched at startup**; on failure `Logger.fatal` → `ShutdownHelper.execute(1)` (oidc.ts:66-69; `server/logging/Logger.ts:178-181`). Outline must start after Keycloak is healthy (`depends_on: condition: service_healthy`).
- With discovery, Outline **overwrites** `OIDC_AUTH_URI`/`OIDC_TOKEN_URI`/`OIDC_USERINFO_URI` from the discovered document (oidc.ts:44-47). All endpoints therefore come from one origin; split internal/public endpoints require manual config. Manual config also never enables PKCE (`oidc.ts:27-35` vs 55-56), while discovery enables it when `code_challenge_methods_supported` includes `S256` (`oidc.ts:55`). Keycloak advertises `plain` + `S256` (`OIDCWellKnownProvider.java:93`), so **Outline will send PKCE**.
- Reachability: the `authorization_endpoint` is used as a browser redirect; token + userinfo are fetched server-side. Keycloak hardcodes endpoint URLs from its `hostname` config (frontend) with static backchannel URLs by default (`https://www.keycloak.org/server/hostname`). If KC frontend host ≠ Outline's reachable host, the browser is sent to the frontend host and they diverge.
- Private IPs: v1.10.0 always passes `allowPrivateIPAddress: true` for discovery and userinfo (`oidcDiscovery.ts:56`, `server/utils/passport.ts:249-251`), fixing issue [#10057](https://github.com/outline/outline/issues/10057); the token exchange is done by passport-oauth2's own client. Compose service names resolving to 172.x are therefore fine. `ALLOWED_PRIVATE_IP_ADDRESSES` exists for other outbound calls (`server/env.ts:852-862`).
- Email validation: `email` must come from the userinfo profile or the (only decoded, **not signature-verified**) id_token (`oidcRouter.ts:106,126-133`). `email_verified` is read from either and coerced to boolean; `true`/`"true"` counts (`oidcRouter.ts:135-142`). An unverified/absent claim is only fatal when an account already exists or the team has allowed domains (`userProvisioner.ts:158-164`).
- `OIDC_USERNAME_CLAIM` is display-only: `name = profile.name || username || profile.username` (`oidcRouter.ts:199-202`); it does not affect account matching.

## 2. Account claiming (source walkthrough)

Login path: OIDC callback → `accountProvisioner` (`accountProvisioner.ts:182-202`) → `userProvisioner`.

Order of lookups in `server/commands/userProvisioner.ts`:

1. **Existing SSO link** by `providerId` (OIDC `sub`) scoped to the team (`:76-90`). Found → update avatar (if not already user-set), overwrite email only if `emailVerified === true`, refresh tokens (`:94-134`). Same user id.
2. **Existing row by email**, case-insensitive `Op.iLike`, same `teamId` (`:144-152`).
3. **Verified gate**: if `emailVerified !== true` and (`existingUser` or team has `allowedDomains`) → `InvalidAuthenticationError` ("Your email address has not been verified…") (`:158-164`).
4. **Claim**: an invite is just a shell `User` with no authentication and no `lastActiveAt` (`isInvited = !lastActiveAt`, `User.ts:281-282`; created by `userInviter.ts:74-97`). On claim: update `name`, `avatarUrl`, `lastActiveAt`, `lastActiveIp`; create a `UserAuthentication` for the existing user; **`user.id` is unchanged and `groupMemberships`/`GroupUser` rows are not touched** (`:168-228`). `isNewUser` is set to `isInvite` (`:227`), so accountProvisioner emits `users.invite_accepted` and may not create the welcome collection (`accountProvisioner.ts:205-230`). Unit tests: `userProvisioner.test.ts:134-165` ("should add authentication provider to invited users"), `:311-338` (invite claimed, case-insensitive email).
5. **No match** → JIT create via `User.createWithCtx` with role `team.defaultUserRole`, unless `inviteRequired` (→ `InviteRequiredError`) or the domain is disallowed (`:241-283`).
6. **Suspended ("deactivated") user**: Outline's flag is `suspendedAt` (`User.ts:205,274-275`); there is no separate deactivated state. `userProvisioner` matches and updates suspended users (no suspended filter in the email lookup), then `signIn`/passport middleware refuse the session with `/?notice=user-suspended` (`server/utils/authentication.ts:50-52`, `server/middlewares/passport.ts:144-146`). Net effect: auth link + profile update happen, **no session, no duplicate**.
7. **Group sync**: only runs if the provider's `settings.groupSyncEnabled` is true **and** a `GroupSyncProvider` hook exists for that provider (`accountProvisioner.ts:232-271`; `PluginManager.ts:133-143`). In v1.10.0 only the Google plugin registers one; the OIDC plugin does not, so OIDC login never mutates Outline groups — memberships survive by construction.
8. Provider/team resolution: on first OIDC login into an existing self-hosted team, `teamProvisioner` creates the `AuthenticationProvider` if the email domain is allowed (`teamProvisioner.ts:98-131`); an empty `allowedDomains` allows everything (`Team.ts:448-464`).

## 3. Keycloak 26.x scripted authorization-code login

Exact browser-equivalent sequence (all refs pinned to `keycloak/keycloak@26.7.3`):

1. `GET {Outline}/auth/oidc` → 302 to Keycloak `authorization_endpoint` (`/realms/{realm}/protocol/openid-connect/auth`) with `response_type=code`, `client_id`, `redirect_uri`, `scope`, `state`, and `code_challenge`+`code_challenge_method=S256` (Outline PKCE). Do not strip params.
2. Follow redirects, keep all cookies (`AUTH_SESSION_ID`, `AUTH_SESSION_ID_LEGACY`, `KC_RESTART`). Keycloak processes the browser flow (`AuthorizationEndpointBase.handleBrowserAuthenticationRequest`, `:107-154`) and renders the login page.
3. Parse **`form#kc-form-login`**'s `action` attribute — do not reconstruct it. It contains `session_code`, `execution`, `client_id`, `tab_id` and (26.x) `client_data` (`AuthenticationProcessor.java:635-656`; `LoginActionsService.java:130,143,324,403`). HTML-unescape `&amp;`.
4. `POST` that action URL as `application/x-www-form-urlencoded` with the parsed URL verbatim and fields `username`, `password`, `credentialId` (empty ok), `login` (`login.ftl:10-77`; username may be username **or** email — `findUserByNameOrEmail`, `AbstractUsernameFormAuthenticator.java:141-165`; password field name = `password`, `CredentialRepresentation.PASSWORD`).
5. Success: 302 to `redirect_uri` with `code` + `state` (no consent, no required actions). Outline's callback exchanges the code server-side (PKCE verifier never leaves Outline), sets an `accessToken` cookie, and redirects into the app (`utils/authentication.ts:148-189`).
6. Failure/extra steps to avoid: required actions (user must have `requiredActions: []`, realm default actions off), consent (client setting), OTP (leave off), brute-force lockout (realm default off).

- **No REST short-circuit exists for the code flow.** The Direct Access Grant (`POST /realms/{realm}/protocol/openid-connect/token`, `grant_type=password`) returns tokens directly and even creates a server-side user session (`ResourceOwnerPasswordCredentialsGrantType.java:97-145`), but it issues no authorization code and sets no browser cookies, so it cannot produce Outline's callback params. Keycloak docs: "Resource owner password credentials grant (Direct Access Grants)" in the Server Administration Guide.
- **Support caveat**: Keycloak docs explicitly say client applications should not directly target `/realms/{realm}/login-actions` (Server Admin Guide, "The preceding steps are the only supported method…"). A test script driving the rendered form is still exercising the real flow; it is unsupported only as a client integration.
- **Brittleness across 26.x**: form field ids/names (`username`, `password`, `credentialId`) and the `authenticate` action structure are stable and covered by Keycloak's own Arquillian login page object (`tests/utils-shared/.../LoginForm.java`). The moving parts are the hidden query params (new `client_data` in 26.x) — solved by parsing the action rather than hardcoding. Risk: intermediate pages when required actions/consent/OTP are enabled; keep the fixture clean.

## 4. Headless browser options & compose DNS

- GitHub Actions `ubuntu-latest` currently maps to Ubuntu 24.04 (runner-images README table); the 24.04 image ships Google Chrome 152 / ChromeDriver 152 (image `20260907.300.1`). So chromedp **can** run on the runner without installing a browser.
- `chromedp/docker-headless-shell` is current: not archived, last push 2026-07-08, 661 stars; image `docker.io/chromedp/headless-shell` with daily `stable`/`beta`/`dev` tags and CDP on `:9222` (README).
- Playwright publishes `mcr.microsoft.com/playwright:v1.63.0-noble` (browsers + system deps; package not included; pin exact version) and supports a remote server: `npx playwright run-server` then connect from tests (`playwright.dev/docs/docker`).
- Compose DNS: user-defined networks run Docker's embedded DNS (`127.0.0.11`) and resolve service/container names; the default bridge does not (`docs.docker.com/engine/network/`). Therefore **any browser/script must execute inside the compose network** for `http://outline:3000` and `http://keycloak:8080` to resolve. Host Chrome needs published ports plus runner `/etc/hosts` shims and still breaks Keycloak's hostname-consistent issuer/redirect URLs. A browser compose service (`chromedp/headless-shell` or Playwright) or the test itself as a one-shot compose service (`docker compose run --rm e2e`) is the clean answer.

## 5. Recommendation matrix

| | (a) Pure-Go scripted HTTP | (b) chromedp + headless-shell service | (c) Playwright service |
| --- | --- | --- | --- |
| Complexity | Low: `net/http`, cookie jar, parse one form action, follow 302s. | Medium: browser service + CDP websocket; selectors `#username`, `#password`. | Medium-high for a Go repo: Node/TS test runner or `run-server` + CDP; version pinning. |
| Brittleness | Medium: couples to Keycloak's rendered form action; mitigated by parsing, pinning 26.7.3, clean fixture. | Low: resilient to markup/action changes as long as field ids hold; still Keycloak UI. | Low: same as (b), plus auto-wait/tracing for debugging. |
| Extra deps | `golang.org/x/net/html` (or regex) only; runs in the existing Go test binary. | ~400 MB headless-shell image; `github.com/chromedp/chromedp`. | ~1 GB Playwright image + Node toolchain; Playwright version must match image. |
| In compose network | Yes, when run as a compose service (`docker compose run --rm e2e`). No browser needed. | Yes: browser container joins network; Go driver can sit in-network or connect to published `:9222`. | Yes: `run-server` as a compose service; connect from test container. |
| Best for | Fast deterministic PR gate; the login page needs no JS. | If HTML coupling is unacceptable and they want a real browser. | Teams already invested in Playwright; overkill here. |

**Recommendation: (a) pure-Go scripted login, executed inside the compose network.** The flow is a single server-rendered form with no JS; parsing the `#kc-form-login` action removes the only fragile part, and it avoids an extra ~400 MB–1 GB image plus CDP plumbing. Keep (b) as the escape hatch if Keycloak theming/required-action pages start appearing.

## 6. Exact settings the e2e would use

Outline (`outlinewiki/outline:1.10.0`, pinned digest in `outline-admin-bootstrap.md`):

```
NODE_ENV=production
URL=http://outline:3000
FORCE_HTTPS=false
SECRET_KEY=<random>
UTILS_SECRET=<random>
DATABASE_URL=postgres://outline:outline@postgres:5432/outline
PGSSLMODE=disable
REDIS_URL=redis://redis:6379
OIDC_CLIENT_ID=outline
OIDC_CLIENT_SECRET=<same as Keycloak client secret>
OIDC_ISSUER_URL=http://keycloak:8080/realms/e2e
OIDC_DISPLAY_NAME=Keycloak
OIDC_USERNAME_CLAIM=preferred_username
OIDC_SCOPES=openid profile email
OIDC_DISABLE_REDIRECT=true
```

Do **not** set `OIDC_AUTH_URI`/`OIDC_TOKEN_URI`/`OIDC_USERINFO_URI` (manual config disables PKCE and skips discovery). `ALLOWED_PRIVATE_IP_ADDRESSES` is not needed for OIDC in v1.10.0.

Keycloak (`quay.io/keycloak/keycloak:26.7.3`, `start-dev`, per `keycloak-disposable-provisioning.md`):

- Realm `e2e`, client `outline`: confidential (`secret`), `standardFlowEnabled=true`, `redirectUris=["http://outline:3000/auth/oidc.callback"]`, `directAccessGrantsEnabled=false`, consent off.
- User: same `email` as the Outline invite (lowercased), `enabled=true`, `emailVerified=true`, `requiredActions=[]`, non-temporary password; realm default required actions (e.g. `VERIFY_PROFILE`, `UPDATE_PASSWORD`) disabled and OTP off, otherwise the scripted POST lands on an action page instead of the callback.
- `KC_HEALTH_ENABLED=true`; Outline `depends_on` Keycloak `service_healthy`. Keep everything on the compose network so `outline`/`keycloak` resolve for server, browser and test alike.

Verification calls (admin uses an API key; no CSRF needed for Bearer):

1. Pre-seed Outline: `POST /api/users.invite` `{"invites":[{"name":"...","email":"...","role":"member"}],"suppressEmail":true}` → record `data.users[0].id`; create group + grant via `POST /api/groups.create` and `POST /api/groups.update_user` (`{"id":group,"userId":invitedId,"permission":"read_write"}`) — route names at `server/routes/api/groups/groups.ts`.
2. Run the scripted login; assert the callback response sets the `accessToken` cookie (`server/utils/authentication.ts:150`).
3. Session proof: `POST /api/auth.info` with `Cookie: accessToken=…` (JSON body `{}`) → `data.user.id` must equal the invited id and `data.groups` must contain the pre-seeded group (`server/routes/api/auth/auth.ts:122-205`). This route is read-only, so CSRF is skipped for cookie auth (`server/middlewares/csrf.ts:61-69`; `shared/helpers/AuthenticationHelper.ts:15-28`).
4. No-duplicate proof: with the admin key, `POST /api/users.list` filtered by the email and assert exactly one user (or diff `users.list` counts before/after).

## Sources

- Outline v1.10.0: [env.ts](https://github.com/outline/outline/blob/v1.10.0/plugins/oidc/server/env.ts), [oidc.ts](https://github.com/outline/outline/blob/v1.10.0/plugins/oidc/server/auth/oidc.ts), [oidcRouter.ts](https://github.com/outline/outline/blob/v1.10.0/plugins/oidc/server/auth/oidcRouter.ts), [oidcDiscovery.ts](https://github.com/outline/outline/blob/v1.10.0/plugins/oidc/server/oidcDiscovery.ts), [userProvisioner.ts](https://github.com/outline/outline/blob/v1.10.0/server/commands/userProvisioner.ts), [accountProvisioner.ts](https://github.com/outline/outline/blob/v1.10.0/server/commands/accountProvisioner.ts), [teamProvisioner.ts](https://github.com/outline/outline/blob/v1.10.0/server/commands/teamProvisioner.ts), [userInviter.ts](https://github.com/outline/outline/blob/v1.10.0/server/commands/userInviter.ts), [User.ts](https://github.com/outline/outline/blob/v1.10.0/server/models/User.ts), [Team.ts](https://github.com/outline/outline/blob/v1.10.0/server/models/Team.ts), [passport.ts](https://github.com/outline/outline/blob/v1.10.0/server/utils/passport.ts), [fetch.ts](https://github.com/outline/outline/blob/v1.10.0/server/utils/fetch.ts), [authentication.ts](https://github.com/outline/outline/blob/v1.10.0/server/utils/authentication.ts), [passport middleware](https://github.com/outline/outline/blob/v1.10.0/server/middlewares/passport.ts), [csrf.ts](https://github.com/outline/outline/blob/v1.10.0/server/middlewares/csrf.ts), [auth.ts](https://github.com/outline/outline/blob/v1.10.0/server/routes/api/auth/auth.ts), [groups.ts](https://github.com/outline/outline/blob/v1.10.0/server/routes/api/groups/groups.ts), [AuthenticationHelper.ts](https://github.com/outline/outline/blob/v1.10.0/shared/helpers/AuthenticationHelper.ts), [.env.sample](https://github.com/outline/outline/blob/v1.10.0/.env.sample), [issue #10057](https://github.com/outline/outline/issues/10057)
- Outline docs: [OIDC](https://docs.getoutline.com/s/hosting/doc/oidc-8CPBm6uC0I), [Configuration](https://docs.getoutline.com/s/hosting/doc/configuration-509J4lAzjo)
- Keycloak 26.7.3: [login.ftl](https://github.com/keycloak/keycloak/blob/26.7.3/themes/src/main/resources/theme/base/login/login.ftl), [AbstractUsernameFormAuthenticator.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/authentication/authenticators/browser/AbstractUsernameFormAuthenticator.java), [AuthenticationProcessor.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/authentication/AuthenticationProcessor.java), [LoginActionsService.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/services/resources/LoginActionsService.java), [AuthorizationEndpointBase.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/protocol/AuthorizationEndpointBase.java), [ResourceOwnerPasswordCredentialsGrantType.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/protocol/oidc/grants/ResourceOwnerPasswordCredentialsGrantType.java), [OIDCLoginProtocolFactory.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/protocol/oidc/OIDCLoginProtocolFactory.java), [OIDCWellKnownProvider.java](https://github.com/keycloak/keycloak/blob/26.7.3/services/src/main/java/org/keycloak/protocol/oidc/OIDCWellKnownProvider.java)
- Keycloak docs: [Hostname (v2)](https://www.keycloak.org/server/hostname), [Server Admin — OIDC endpoints](https://www.keycloak.org/docs/26.7.3/server_admin/), [Direct Access Grants](https://www.keycloak.org/docs/26.7.3/server_admin/#_oidc-auth-flows-direct)
- Docker: [Networking overview](https://docs.docker.com/engine/network/)
- GitHub: [runner-images README](https://github.com/actions/runner-images#available-images), [Ubuntu 24.04 image](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md), [chromedp/docker-headless-shell](https://github.com/chromedp/docker-headless-shell), [Playwright Docker](https://playwright.dev/docs/docker)
