# Keycloak Admin REST API for mirroring groups/client-roles into Outline

Verified 2026-09-15 against Keycloak **26.7.3** (latest release) and `main` (26.8-dev). Sources at the end.

## 1. Service-to-service auth and required realm-management roles

Token: `POST /realms/{realm}/protocol/openid-connect/token` with `grant_type=client_credentials`; use a dedicated confidential client with *Client authentication* + *Service account roles* on. Credentials via Basic `clientId:clientSecret` or `client_id`/`client_secret` form params.[3]

Admin roles live on the client whose client ID is literally **`realm-management`** (in the master realm: `{realm}-realm`). `AdminRoles` has `view-users`, `view-clients`, `view-events`, `manage-users`, `manage-clients`, `manage-events`, `query-users`, `query-clients`, `query-realms`, `query-groups`, `query-organizations`, `impersonation`, `realm-admin`. **There is no `view-groups` role** — group reads are covered by `view-users`/`manage-users`; `query-groups` only enables search/console listing.[4][5]

| Operation | Endpoint | Role(s) |
|---|---|---|
| Find user by email | `GET /users?email=…&exact=true` | `query-users` for the query itself; actual user data requires `view-users`/`manage-users` (`view-users` also satisfies `canQuery`)[5][8] |
| User effective client roles | `GET /users/{id}/role-mappings/clients/{client-uuid}/composite` | `view-users`/`manage-users` + `view-clients`/`manage-clients`[5][6][8] |
| Client's roles / resolve clientId→UUID | `GET /clients/{client-uuid}/roles`, `GET /clients?clientId=…` | `view-clients`/`manage-clients`; `query-clients` (or `query-users`) also permits client listing[8] |
| List/search groups | `GET /groups` | `query-groups` **or** `view-users`/`manage-users`[5][8] |
| Group members | `GET /groups/{id}/members` | `view-users`/`manage-users`[5][8] |
| Group role mappings | `GET /groups/{id}/role-mappings[/clients/{client-uuid}]` | `view-users`/`manage-users`; client-specific path additionally `view-clients`/`manage-clients`[5][6][8] |
| Admin events | `GET /admin-events` | `view-events` (or `manage-events`)[9] |

Practical minimum for read-only mirroring: `view-users`, `view-clients` (both cover search/list too); add `view-events` only if polling events. `query-*` alone does **not** grant reading.

Fine-grained admin permissions v2: introduced in 26.2, enabled **per realm** (`adminPermissionsEnabled`, creates the `admin-permissions` client). Classic admin roles bypass fine-grained evaluation; without them, explicit permission scopes (`view`, `view-members`, `manage`, …) are required. v1 (`ADMIN_FINE_GRAINED_AUTHZ`) is **deprecated**; v2 (`ADMIN_FINE_GRAINED_AUTHZ_V2`) is a DEFAULT feature in 26.7.3/main.[10][11]

## 2. Endpoints and semantics

- `GET /users`: `email`/`username`/`firstName`/`lastName` are substring matches unless `exact=true` (then complete match). `search` is prefix by default, `*foo*` infix, `"foo"` exact. `first` defaults to 0, `max` to 100 (`Constants.DEFAULT_MAX_RESULTS`); no documented hard cap in code. Unfiltered listing excludes service-account users, while `email`/`username` filters include them.[1][5]
- `GET /users/{id}/role-mappings/clients/{uuid}` = **direct** mappings only. `…/composite` = effective set: direct + roles of **all groups the user belongs to, including ancestor groups**, with composite roles expanded (`RoleUtils.getDeepUserRoleMappings`). This is the endpoint that includes group-inherited client roles.[5][6]
- `fullScopeAllowed` only limits roles placed in tokens/scope mappings; the admin role-mapping reads do not consult it.[12][6]
- `GET /groups`: `briefRepresentation` default true; `first`/`max` have no defaults — omitted, all top-level groups are returned. `subGroups` are populated only with `search` or `q` (`populateHierarchy` default true). `GET /groups/{id}/children` defaults `first=0`, **`max=10`**. `GET /groups/{id}/members` returns **direct members only** (JPA predicate `groupId = group`); enumerate nested groups via `/children` or `search`/`q`.[1][5][7]
- Group role mappings: non-composite = direct group mappings. `…/clients/{uuid}/composite` expands composites of the group's direct mappings but does **not** include mappings inherited from parent groups (unlike realm `/composite` in ≤26.7, which used `GroupModel.hasRole` walking parents). Ancestor roles are inherited by members at authorization time (docs; `UserAdapter.hasRole`/`GroupAdapter.hasRole` parent walk).[5][6]

## 3. Most reliable enumeration

Per-user composite mappings: page `GET /users` (`max=100`+) and call `/users/{id}/role-mappings/clients/{uuid}/composite` per user. It is authoritative for direct + group + ancestor + composite roles. Cost: N+1 requests for N users, each response up to all client roles. Group-centric iteration (per-group mappings + members) is cheaper when groups ≪ users but must expand composites, walk ancestor groups, and recurse child groups, since members are direct-only. `GET /clients/{uuid}/roles/{role-name}/users` and `…/groups` return **direct holders only** (no group inheritance, no composites) — usable as a candidate set, then verify per user.[1][5][6][7] For incremental updates, prefer admin-events polling.

## 4. Events/webhooks

No built-in HTTP webhook: Keycloak ships an Event Listener SPI (built-ins: log/email), no HTTP push. Admin events API `GET /admin/realms/{realm}/admin-events` filters by `operationTypes`, `resourceTypes` (enum names: `CLIENT_ROLE_MAPPING`, `REALM_ROLE_MAPPING`, `GROUP_MEMBERSHIP`, `USER`, `GROUP`, …), `resourcePath`, `authUser`, `dateFrom`/`dateTo` (yyyy-MM-dd or epoch ms), `direction`, `first`, `max` (default 100). It records admin-API/console actions (role mapping add/remove → `*_ROLE_MAPPING` CREATE/DELETE; join/leave group → `GROUP_MEMBERSHIP`), only when realm admin events are enabled; direct DB changes, imports, and LDAP sync are not captured. Standard approach: poll with a `dateFrom`/`dateTo` cursor; use a custom Java listener SPI if true push is needed.[3][9][11]

## 5. Gotchas

- Quarkus distribution (17+) has no `/auth` prefix; WildFly ≤16 used `/auth/admin/realms/...`.[13]
- Role-mapping paths take the client **UUID**, not `clientId`.
- Master realm admin client is `{realm}-realm`, not `realm-management`.
- Nested groups: membership is direct; role mappings cascade to descendants.
- `/groups/{id}/children` silently caps at 10 per page if `max` omitted.
- Group client-composite parent inheritance is inconsistent with realm composite; walk ancestors yourself.
- Flagged unverified: no server-side upper cap on `max` was found, but DB/driver limits may apply; FGAP v2 status could change in later 26.x releases.

## Sources

1. REST API reference — https://www.keycloak.org/docs-api/latest/rest-api/ (spec: `openapi.json`)
2. Server Admin Guide — https://www.keycloak.org/docs/latest/server_admin/
3. Service accounts — https://www.keycloak.org/docs/latest/server_admin/#_service_accounts
4. `AdminRoles.java`, `Constants.java` — https://github.com/keycloak/keycloak/blob/main/server-spi-private/src/main/java/org/keycloak/models/AdminRoles.java, `…/models/Constants.java`
5. `UsersResource`, `GroupsResource`, `GroupResource`, `RoleMapperResource`, `ClientRoleMappingsResource` — https://github.com/keycloak/keycloak/tree/main/services/src/main/java/org/keycloak/services/resources/admin
6. `RoleUtils.java` — https://github.com/keycloak/keycloak/blob/main/server-spi/src/main/java/org/keycloak/models/utils/RoleUtils.java
7. `JpaUserProvider.java`, `UserAdapter.java`, `GroupAdapter.java` — https://github.com/keycloak/keycloak/tree/main/model/jpa/src/main/java/org/keycloak/models/jpa
8. fgap evaluators — https://github.com/keycloak/keycloak/tree/main/services/src/main/java/org/keycloak/services/resources/admin/fgap
9. `RealmAdminResource.java`, `ResourceType.java` — same repos as [5], plus `server-spi-private/.../events/admin/ResourceType.java`
10. `fine-grain-v2.adoc` — https://github.com/keycloak/keycloak/blob/main/docs/documentation/server_admin/topics/admin-console-permissions/fine-grain-v2.adoc
11. 26.2 release notes, `Profile.java` — https://github.com/keycloak/keycloak/blob/main/docs/documentation/release_notes/topics/26_2_0.adoc, `…/common/.../Profile.java`
12. Role scope mappings / Full Scope Allowed — https://www.keycloak.org/docs/latest/server_admin/#_role_scope_mappings
13. 17.0.0 release notes / migration — https://github.com/keycloak/keycloak/blob/main/docs/documentation/release_notes/topics/17_0_0.adoc, https://www.keycloak.org/migration/migrating-to-quarkus
