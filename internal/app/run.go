package app

import (
	"context"
	"errors"
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
	roleHolders, skippedUsers, err := findRoleHolders(ctx, keycloak, users, rolesUUID, logger)
	if err != nil {
		return err
	}

	outline := newOutlineClient(cfg.outlineURL, cfg.outlineToken)
	groups, err := outline.listGroups(ctx)
	if err != nil {
		return fmt.Errorf("listing Outline Groups: %w", err)
	}

	externalIDPrefix := managedExternalIDPrefix(cfg)
	actions := planGroupActions(roles, groups, externalIDPrefix)
	orphanedGroups := findOrphanedGroups(groups, roles, externalIDPrefix)
	accounts, err := resolveAccounts(ctx, outline, actions, roleHolders)
	if err != nil {
		return err
	}
	memberships, err := readMemberships(ctx, outline, actions)
	if err != nil {
		return err
	}

	for _, group := range orphanedGroups {
		logger.Warn("orphaned Managed Group",
			"group", group.Name,
			"externalId", group.ExternalID,
		)
	}

	summary := runSummary{skippedUsers: skippedUsers, orphanedGroups: len(orphanedGroups)}
	failures := applyPlan(ctx, outline, actions, roles, memberships, roleHolders, accounts, logger, &summary)

	logger.Info("sync run complete",
		"groupsCreated", summary.groupsCreated,
		"groupsAdopted", summary.groupsAdopted,
		"groupsRenamed", summary.groupsRenamed,
		"membersAdded", summary.membersAdded,
		"membersRemoved", summary.membersRemoved,
		"skippedUsers", summary.skippedUsers,
		"orphanedGroups", summary.orphanedGroups,
		"failures", len(failures),
	)
	if len(failures) > 0 {
		return fmt.Errorf("%d Outline operation(s) failed: %w", len(failures), errors.Join(failures...))
	}
	return nil
}

type runSummary struct {
	groupsCreated  int
	groupsAdopted  int
	groupsRenamed  int
	membersAdded   int
	membersRemoved int
	skippedUsers   int
	orphanedGroups int
}

func findRoleHolders(
	ctx context.Context,
	keycloak *keycloakAdmin,
	users []keycloakUser,
	rolesUUID string,
	logger *slog.Logger,
) (map[string][]keycloakUser, int, error) {
	var candidates []keycloakUser
	skippedUsers := 0
	emailCount := map[string]int{}
	for _, user := range users {
		if !user.Enabled {
			logger.Warn("skipping Keycloak user",
				"reason", "disabled",
				"username", user.Username,
			)
			skippedUsers++
			continue
		}
		if user.Email == "" {
			logger.Warn("skipping Keycloak user",
				"reason", "no email",
				"username", user.Username,
			)
			skippedUsers++
			continue
		}
		email := normalizeEmail(user.Email)
		emailCount[email]++
		candidates = append(candidates, user)
	}

	holders := map[string][]keycloakUser{}
	for _, user := range candidates {
		email := normalizeEmail(user.Email)
		if emailCount[email] > 1 {
			logger.Warn("skipping Keycloak user",
				"reason", "duplicate email",
				"username", user.Username,
				"email", user.Email,
			)
			skippedUsers++
			continue
		}
		mappings, err := keycloak.effectiveClientRoles(ctx, user.ID, rolesUUID)
		if err != nil {
			return nil, 0, fmt.Errorf("reading Client Roles of user %q: %w", user.Username, err)
		}
		for _, role := range mappings {
			holders[role.ID] = append(holders[role.ID], user)
		}
	}
	return holders, skippedUsers, nil
}

func resolveAccounts(
	ctx context.Context,
	outline *outlineClient,
	actions []groupAction,
	roleHolders map[string][]keycloakUser,
) (map[string]outlineUser, error) {
	var emails []string
	seen := map[string]bool{}
	for _, action := range actions {
		for _, holder := range roleHolders[action.roleID] {
			email := normalizeEmail(holder.Email)
			if !seen[email] {
				seen[email] = true
				emails = append(emails, email)
			}
		}
	}

	accounts := map[string]outlineUser{}
	if len(emails) == 0 {
		return accounts, nil
	}
	found, err := outline.listUsersByEmails(ctx, emails)
	if err != nil {
		return nil, fmt.Errorf("resolving Outline Accounts: %w", err)
	}
	for _, account := range found {
		accounts[normalizeEmail(account.Email)] = account
	}
	return accounts, nil
}

func readMemberships(ctx context.Context, outline *outlineClient, actions []groupAction) (map[string][]string, error) {
	memberships := map[string][]string{}
	for _, action := range actions {
		if action.kind == groupActionCreate {
			continue
		}
		memberIDs, err := outline.groupMemberIDs(ctx, action.groupID)
		if err != nil {
			return nil, fmt.Errorf("listing members of Outline Group %q: %w", action.name, err)
		}
		memberships[action.groupID] = memberIDs
	}
	return memberships, nil
}

func findOrphanedGroups(groups []outlineGroup, roles []clientRole, externalIDPrefix string) []outlineGroup {
	roleIDs := map[string]bool{}
	for _, role := range roles {
		roleIDs[role.ID] = true
	}

	var orphaned []outlineGroup
	for _, group := range groups {
		if !strings.HasPrefix(group.ExternalID, externalIDPrefix) {
			continue
		}
		if roleIDs[strings.TrimPrefix(group.ExternalID, externalIDPrefix)] {
			continue
		}
		orphaned = append(orphaned, group)
	}
	return orphaned
}

func applyPlan(
	ctx context.Context,
	outline *outlineClient,
	actions []groupAction,
	roles []clientRole,
	memberships map[string][]string,
	roleHolders map[string][]keycloakUser,
	accounts map[string]outlineUser,
	logger *slog.Logger,
	summary *runSummary,
) []error {
	var failures []error

	groupsByRole := map[string]outlineGroup{}
	for _, action := range actions {
		group, err := applyGroupAction(ctx, outline, action, summary)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		groupsByRole[action.roleID] = group
	}

	warnedNoAccount := map[string]bool{}
	for _, role := range roles {
		group, applied := groupsByRole[role.ID]
		if !applied {
			continue
		}
		desired := desiredMemberIDs(roleHolders[role.ID], accounts, logger, summary, warnedNoAccount)
		if err := reconcileMembers(ctx, outline, group, memberships[group.ID], desired, summary); err != nil {
			failures = append(failures, err)
		}
	}
	return failures
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

func desiredMemberIDs(
	holders []keycloakUser,
	accounts map[string]outlineUser,
	logger *slog.Logger,
	summary *runSummary,
	warnedNoAccount map[string]bool,
) []string {
	var ids []string
	seen := map[string]bool{}
	for _, holder := range holders {
		account, found := accounts[normalizeEmail(holder.Email)]
		if !found {
			if !warnedNoAccount[holder.ID] {
				warnedNoAccount[holder.ID] = true
				summary.skippedUsers++
				logger.Warn("skipping Keycloak user",
					"reason", "no Outline Account",
					"username", holder.Username,
					"email", holder.Email,
				)
			}
			continue
		}
		if !seen[account.ID] {
			seen[account.ID] = true
			ids = append(ids, account.ID)
		}
	}
	return ids
}

func reconcileMembers(
	ctx context.Context,
	outline *outlineClient,
	group outlineGroup,
	current []string,
	desired []string,
	summary *runSummary,
) error {
	currentMembers := map[string]bool{}
	for _, userID := range current {
		currentMembers[userID] = true
	}
	desiredMembers := map[string]bool{}
	for _, userID := range desired {
		desiredMembers[userID] = true
	}

	var failures []error
	for _, userID := range current {
		if desiredMembers[userID] {
			continue
		}
		if err := outline.removeUserFromGroup(ctx, group.ID, userID); err != nil {
			failures = append(failures, fmt.Errorf("removing %s from Outline Group %q: %w", userID, group.Name, err))
			continue
		}
		summary.membersRemoved++
	}
	for _, userID := range desired {
		if currentMembers[userID] {
			continue
		}
		if err := outline.addUserToGroup(ctx, group.ID, userID); err != nil {
			failures = append(failures, fmt.Errorf("adding %s to Outline Group %q: %w", userID, group.Name, err))
			continue
		}
		summary.membersAdded++
	}
	return errors.Join(failures...)
}

func normalizeEmail(email string) string {
	return strings.ToLower(email)
}
