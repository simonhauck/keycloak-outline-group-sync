package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

func runSync(ctx context.Context, cfg config, logger *slog.Logger) error {
	keycloak := newKeycloakAdmin(cfg.keycloakURL, cfg.keycloakRealm)
	if err := keycloak.authenticate(ctx, cfg.keycloakClientID, cfg.keycloakClientSecret); err != nil {
		return fmt.Errorf("keycloak authentication failed: %w", err)
	}
	rolesUUID, err := keycloak.clientUUID(ctx, cfg.rolesClientID)
	if err != nil {
		return fmt.Errorf("resolving roles client %q: %w", cfg.rolesClientID, err)
	}
	roles, err := keycloak.clientRoles(ctx, rolesUUID)
	if err != nil {
		return fmt.Errorf("listing Client Roles of %q: %w", cfg.rolesClientID, err)
	}
	users, err := keycloak.users(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}

	outline := newOutlineClient(cfg.outlineURL, cfg.outlineToken)
	groups, err := outline.listGroups(ctx)
	if err != nil {
		return fmt.Errorf("listing Outline Groups: %w", err)
	}

	summary := runSummary{}
	roleGroups := map[string]outlineGroup{}
	for _, action := range planGroupActions(roles, groups, managedExternalIDPrefix(cfg)) {
		group, err := applyGroupAction(ctx, outline, action, &summary)
		if err != nil {
			return err
		}
		roleGroups[action.roleID] = group
	}

	roleHolders, err := findRoleHolders(ctx, keycloak, users, rolesUUID, roleGroups, logger)
	if err != nil {
		return err
	}
	if err := addMissingMembers(ctx, outline, roles, roleGroups, roleHolders, logger, &summary); err != nil {
		return err
	}

	logger.Info("sync run complete",
		"groupsCreated", summary.groupsCreated,
		"groupsAdopted", summary.groupsAdopted,
		"groupsRenamed", summary.groupsRenamed,
		"membersAdded", summary.membersAdded,
	)
	return nil
}

type runSummary struct {
	groupsCreated int
	groupsAdopted int
	groupsRenamed int
	membersAdded  int
}

func applyGroupAction(ctx context.Context, outline *outlineClient, action groupAction, summary *runSummary) (outlineGroup, error) {
	switch action.kind {
	case groupActionCreate:
		group, err := outline.createGroup(ctx, action.name, action.externalID)
		if err != nil {
			return outlineGroup{}, fmt.Errorf("creating Managed Group %q: %w", action.name, err)
		}
		summary.groupsCreated++
		return group, nil
	case groupActionAdopt:
		group, err := outline.updateGroup(ctx, action.groupID, action.name, action.externalID)
		if err != nil {
			return outlineGroup{}, fmt.Errorf("adopting Outline Group as %q: %w", action.name, err)
		}
		summary.groupsAdopted++
		return group, nil
	case groupActionRename:
		group, err := outline.updateGroup(ctx, action.groupID, action.name, action.externalID)
		if err != nil {
			return outlineGroup{}, fmt.Errorf("renaming Managed Group to %q: %w", action.name, err)
		}
		summary.groupsRenamed++
		return group, nil
	default:
		return outlineGroup{ID: action.groupID, Name: action.name, ExternalID: action.externalID}, nil
	}
}

func findRoleHolders(
	ctx context.Context,
	keycloak *keycloakAdmin,
	users []clientUser,
	rolesUUID string,
	managedRoles map[string]outlineGroup,
	logger *slog.Logger,
) (map[string][]clientUser, error) {
	var candidates []clientUser
	emailCount := map[string]int{}
	for _, user := range users {
		if !user.Enabled {
			logger.Warn("skipping Keycloak user",
				"reason", "disabled",
				"username", user.Username,
			)
			continue
		}
		if user.Email == "" {
			logger.Warn("skipping Keycloak user",
				"reason", "no email",
				"username", user.Username,
			)
			continue
		}
		email := strings.ToLower(user.Email)
		emailCount[email]++
		candidates = append(candidates, user)
	}

	holders := map[string][]clientUser{}
	for _, user := range candidates {
		email := strings.ToLower(user.Email)
		if emailCount[email] > 1 {
			logger.Warn("skipping Keycloak user",
				"reason", "duplicate email",
				"username", user.Username,
				"email", user.Email,
			)
			continue
		}
		mappings, err := keycloak.effectiveClientRoles(ctx, user.ID, rolesUUID)
		if err != nil {
			return nil, fmt.Errorf("reading Client Roles of user %q: %w", user.Username, err)
		}
		for _, role := range mappings {
			if _, managed := managedRoles[role.ID]; managed {
				holders[role.ID] = append(holders[role.ID], user)
			}
		}
	}
	return holders, nil
}

func addMissingMembers(
	ctx context.Context,
	outline *outlineClient,
	roles []clientRole,
	roleGroups map[string]outlineGroup,
	roleHolders map[string][]clientUser,
	logger *slog.Logger,
	summary *runSummary,
) error {
	var accounts []outlineUser
	if emails := holderEmails(roles, roleHolders); len(emails) > 0 {
		var err error
		accounts, err = outline.listUsersByEmails(ctx, emails)
		if err != nil {
			return fmt.Errorf("resolving Outline Accounts: %w", err)
		}
	}
	accountByEmail := map[string]outlineUser{}
	for _, account := range accounts {
		accountByEmail[strings.ToLower(account.Email)] = account
	}

	for _, role := range roles {
		group := roleGroups[role.ID]
		memberIDs, err := outline.groupMemberIDs(ctx, group.ID)
		if err != nil {
			return fmt.Errorf("listing members of Outline Group %q: %w", group.Name, err)
		}
		members := map[string]bool{}
		for _, memberID := range memberIDs {
			members[memberID] = true
		}

		for _, holder := range roleHolders[role.ID] {
			account, found := accountByEmail[strings.ToLower(holder.Email)]
			if !found {
				logger.Warn("skipping Keycloak user",
					"reason", "no Outline Account",
					"username", holder.Username,
					"email", holder.Email,
				)
				continue
			}
			if members[account.ID] {
				continue
			}
			if err := outline.addUserToGroup(ctx, group.ID, account.ID); err != nil {
				return fmt.Errorf("adding %q to Outline Group %q: %w", holder.Email, group.Name, err)
			}
			members[account.ID] = true
			summary.membersAdded++
		}
	}
	return nil
}

func holderEmails(roles []clientRole, roleHolders map[string][]clientUser) []string {
	var emails []string
	seen := map[string]bool{}
	for _, role := range roles {
		for _, holder := range roleHolders[role.ID] {
			email := strings.ToLower(holder.Email)
			if !seen[email] {
				seen[email] = true
				emails = append(emails, email)
			}
		}
	}
	return emails
}
