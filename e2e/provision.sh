#!/usr/bin/env bash
set -euo pipefail

KCADM=/opt/keycloak/bin/kcadm.sh
SERVER=http://keycloak:8080
REALM=e2e
ROLES_CLIENT=roles-client

$KCADM config credentials --server "$SERVER" --realm master --user admin --password admin

$KCADM create realms -s realm=$REALM -s enabled=true -s duplicateEmailsAllowed=true

# OIDC client for Outline SSO (exercised by the SSO scenarios).
$KCADM create clients -r $REALM \
  -s clientId=outline -s enabled=true -s publicClient=false \
  -s clientAuthenticatorType=client-secret -s secret=outline-secret \
  -s standardFlowEnabled=true -s directAccessGrantsEnabled=false \
  -s 'redirectUris=["http://localhost:13000/auth/oidc.callback"]'

# Client that carries the Client Roles mapped to Outline Groups.
ROLES_CID=$($KCADM create clients -r $REALM \
  -s clientId=$ROLES_CLIENT -s enabled=true -s publicClient=false \
  -s clientAuthenticatorType=client-secret -s secret=roles-secret -i)

# Service account used by the Sync Service (view-only realm-management roles).
$KCADM create clients -r $REALM \
  -s clientId=sync-client -s enabled=true -s publicClient=false \
  -s clientAuthenticatorType=client-secret -s secret=sync-secret \
  -s serviceAccountsEnabled=true -s standardFlowEnabled=false

$KCADM add-roles -r $REALM --uusername service-account-sync-client \
  --cclientid realm-management --rolename view-users --rolename view-clients

for role in team-create team-adopt team-rename team-members team-direct team-disabled team-duplicate team-noaccount team-excluded team-sso; do
  $KCADM create clients/$ROLES_CID/roles -r $REALM -s name=$role
done

create_user() {
  local username=$1 email=$2 enabled=${3:-true}
  $KCADM create users -r $REALM -s username=$username -s email=$email \
    -s enabled=$enabled -s emailVerified=true \
    -s firstName=$username -s lastName=User -i
}

ALICE_ID=$(create_user alice alice@example.com)
BOB_ID=$(create_user bob bob@example.com)
CAROL_ID=$(create_user carol carol@example.com)
DAVE_ONE_ID=$(create_user dave-one dave@example.com)
DAVE_TWO_ID=$(create_user dave-two dave@example.com)
ERIN_ID=$(create_user erin erin@example.com)
GRACE_ID=$(create_user grace grace@example.com false)
JANE_ID=$(create_user jane jane@example.com)
create_user admin-sso admin@example.com >/dev/null
create_user frank frank@example.com >/dev/null

$KCADM add-roles -r $REALM --uid $BOB_ID --cclientid $ROLES_CLIENT --rolename team-direct
$KCADM add-roles -r $REALM --uid $DAVE_ONE_ID --cclientid $ROLES_CLIENT --rolename team-duplicate
$KCADM add-roles -r $REALM --uid $DAVE_TWO_ID --cclientid $ROLES_CLIENT --rolename team-duplicate
$KCADM add-roles -r $REALM --uid $ERIN_ID --cclientid $ROLES_CLIENT --rolename team-noaccount
$KCADM add-roles -r $REALM --uid $GRACE_ID --cclientid $ROLES_CLIENT --rolename team-excluded
$KCADM add-roles -r $REALM --uid $JANE_ID --cclientid $ROLES_CLIENT --rolename team-sso

# Users that sign in through the SSO scenarios.
$KCADM set-password -r $REALM --username jane --new-password jane-password
$KCADM set-password -r $REALM --username admin-sso --new-password admin-password

MEMBERS_GID=$($KCADM create groups -r $REALM -s name=g-members -i)
$KCADM add-roles -r $REALM --gid $MEMBERS_GID --cclientid $ROLES_CLIENT --rolename team-members
$KCADM update users/$ALICE_ID/groups/$MEMBERS_GID -r $REALM -n

DISABLED_GID=$($KCADM create groups -r $REALM -s name=g-disabled -i)
$KCADM add-roles -r $REALM --gid $DISABLED_GID --cclientid $ROLES_CLIENT --rolename team-disabled
$KCADM update users/$CAROL_ID/groups/$DISABLED_GID -r $REALM -n

echo "provisioned realm $REALM"
