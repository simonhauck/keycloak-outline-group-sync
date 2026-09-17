# Keycloak is the source of truth for managed group membership

For every Managed Group, the service mirrors the Client Role's Role Holders exactly: missing users are added and extra users are removed. Destructive Full State Sync was chosen over additive sync because access must be revoked in Outline when the corresponding Keycloak role assignment is revoked, and additive sync would leave stale access behind. Consequence: manual membership edits to Managed Groups in Outline are overwritten on the next Sync Run; unmanaged Outline Groups are never touched.
