# Standalone sync service instead of Outline's built-in group sync

Outline ships a group-sync hook, but as of 1.10 no open-source provider maps OIDC group claims, and the hook only fires on SSO login — so revoked access would linger until the user's next login. We run a standalone Go service that polls the Keycloak Admin API and reconciles Outline through its admin API instead: stock Outline image, reconciliation independent of login activity, testable in isolation. The rejected alternative is a custom Outline plugin shipped in a forked image.
