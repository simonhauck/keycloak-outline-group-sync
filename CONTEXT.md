# Keycloak → Outline Group Sync

A self-hosted service that keeps Outline group membership in sync with Keycloak, so access to Outline collections is granted through Keycloak group membership instead of being maintained twice.

## Language

**Sync Service**:
The long-running container that reconciles Outline Group membership from Keycloak.
_Avoid_: bridge, connector, integration, sidecar

**Sync Run**:
One complete reconciliation pass over every Managed Group.
_Avoid_: job, cycle, iteration

**Keycloak Client**:
The Keycloak client whose Client Roles define which Outline Groups are managed.
_Avoid_: app, application, OIDC client

**Client Role**:
A Keycloak role defined on the Keycloak Client. Each managed Outline Group corresponds to one Client Role.
_Avoid_: role (unqualified), permission

**Keycloak Group**:
A group in the source realm, used to bundle Client Roles for its members.
_Avoid_: team, org

**Role Holder**:
A Keycloak user who holds a Client Role, whether through Keycloak Group membership or direct assignment.
_Avoid_: member, grantee

**Outline Account**:
A user record in Outline, created by that person logging in through SSO. The Sync Service never creates one.
_Avoid_: Outline user, seat

**Outline Group**:
A group in Outline that grants its members access to collections.
_Avoid_: team

**Managed Group**:
An Outline Group whose membership the Sync Service owns and reconciles. Outline Groups that are not managed are never touched.
_Avoid_: synced group, mirrored group

**Full State Sync**:
The reconciliation mode in which Outline Group membership is mirrored exactly: missing Role Holders are added and extra members are removed.
_Avoid_: additive sync, one-way sync, incremental sync

**Orphaned Managed Group**:
A Managed Group whose Client Role no longer exists on the Keycloak Client. The Sync Service leaves it and its members untouched and reports it on each Sync Run.
_Avoid_: stale group, leftover group, abandoned group
