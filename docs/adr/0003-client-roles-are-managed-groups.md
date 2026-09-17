# Every Client Role of the configured client is a Managed Group

The service keeps no allowlist of roles: every Client Role on the configured Keycloak Client maps one-to-one to an Outline Group named after the role. This was chosen so onboarding a group is a pure Keycloak operation — create the role, assign it to a Keycloak Group, done — and so operators can either reuse their OIDC client or run a dedicated one, at their discretion. Consequence: any role added to the configured client becomes a Managed Group on the next Sync Run, so unrelated roles must live on a different client.
