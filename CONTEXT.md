# Keycloak → Outline Group Sync

A self-hosted service that keeps Outline group membership and Team Roles in sync with Keycloak, so access to Outline collections is granted through Keycloak group membership instead of being maintained twice.

## Language

**Sync Service**:
The long-running container that reconciles Outline Group membership and Team Roles from Keycloak.
_Avoid_: bridge, connector, integration, sidecar

**Sync Run**:
One complete reconciliation pass over every Managed Group and Managed Account.
_Avoid_: job, cycle, iteration

**Keycloak Client**:
The Keycloak client whose Client Roles define which Outline Groups are managed.
_Avoid_: app, application, OIDC client

**Client Role**:
A Keycloak role defined on the Keycloak Client. Unless it is a Role Signal, it corresponds one-to-one to a Managed Group.
_Avoid_: role (unqualified), permission

**Role Signal**:
A Client Role that maps to a Team Role instead of a Managed Group. Keycloak users holding it have their Team Role computed from it.
_Avoid_: marker role, mapping role, special role

**Keycloak Group**:
A group in the source realm, used to bundle Client Roles for its members.
_Avoid_: team, org

**Role Holder**:
A Keycloak user who holds a Client Role, whether through Keycloak Group membership or direct assignment.
_Avoid_: member, grantee

**Outline Account**:
A user record in Outline, created by that person logging in through SSO. The Sync Service never creates one.
_Avoid_: Outline user, seat

**Managed Account**:
An Outline Account whose Team Role the Sync Service computes: one matched by email to exactly one enabled Keycloak user, or to only disabled Keycloak users. Unmatched and ambiguously matched Outline Accounts are never touched.
_Avoid_: synced user, mirrored user

**Team Role**:
The role an Outline Account holds inside the Outline team: `Admin`, `Member`, `Viewer` or `Guest`. Distinct from a Client Role.
_Avoid_: application role, Outline role, permission

**Fallback Team Role**:
The Team Role given to a Managed Account that matches no Role Signal: `Viewer` by default, configurable.
_Avoid_: default role, base role

**Outline Group**:
A group in Outline that grants its members access to collections.
_Avoid_: team

**Managed Group**:
An Outline Group whose membership the Sync Service owns and reconciles. Outline Groups that are not managed are never touched.
_Avoid_: synced group, mirrored group

**Full State Sync**:
The reconciliation mode in which the Sync Service mirrors Outline state exactly: Managed Group membership is set to the Role Holders and Team Roles are set to match the Role Signals, with the Fallback Team Role when none match.
_Avoid_: additive sync, one-way sync, incremental sync

**Orphaned Managed Group**:
A Managed Group whose Client Role no longer exists on the Keycloak Client or has become a Role Signal. The Sync Service leaves it and its members untouched and reports it on each Sync Run.
_Avoid_: stale group, leftover group, abandoned group
