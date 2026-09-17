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
	scan, err := findRoleHolders(ctx, keycloak, users, rolesUUID, logger)
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

	summary := runSummary{skippedUsers: scan.skippedUsers, orphanedGroups: len(orphanedGroups)}
	accounts, err := resolveAccounts(ctx, outline, roles, scan)
	if err != nil {
		return err
	}
	desiredByRole, protectedAccounts := planDesiredMembers(roles, scan, accounts, logger, &summary)
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

	failures := applyPlan(ctx, outline, actions, roles, memberships, desiredByRole, protectedAccounts, logger, &summary, cfg.dryRun)

	message := "sync run complete"
	if cfg.dryRun {
		message = "dry run complete: no writes were made"
	}
	logRunSummary(logger, message, &summary, len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("%d Outline operation(s) failed: %w", len(failures), errors.Join(failures...))
	}
	return nil
}

func logRunSummary(logger *slog.Logger, message string, summary *runSummary, failures int) {
	logger.Info(message,
		"groupsCreated", summary.groupsCreated,
		"groupsAdopted", summary.groupsAdopted,
		"groupsRenamed", summary.groupsRenamed,
		"membersAdded", summary.membersAdded,
		"membersRemoved", summary.membersRemoved,
		"skippedUsers", summary.skippedUsers,
		"orphanedGroups", summary.orphanedGroups,
		"failures", failures,
	)
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

type roleHolderScan struct {
	holders         map[string][]keycloakUser
	ambiguousEmails map[string]bool
	skippedUsers    int
}

func findRoleHolders(
	ctx context.Context,
	keycloak *keycloakAdmin,
	users []keycloakUser,
	rolesUUID string,
	logger *slog.Logger,
) (roleHolderScan, error) {
	scan := roleHolderScan{
		holders:         map[string][]keycloakUser{},
		ambiguousEmails: map[string]bool{},
	}

	var candidates []keycloakUser
	emailCount := map[string]int{}
	for _, user := range users {
		if !user.Enabled {
			logger.Warn("skipping Keycloak user",
				"reason", "disabled",
				"username", user.Username,
			)
			scan.skippedUsers++
			continue
		}
		if user.Email == "" {
			logger.Warn("skipping Keycloak user",
				"reason", "no email",
				"username", user.Username,
			)
			scan.skippedUsers++
			continue
		}
		email := normalizeEmail(user.Email)
		emailCount[email]++
		candidates = append(candidates, user)
	}

	for _, user := range candidates {
		email := normalizeEmail(user.Email)
		if emailCount[email] > 1 {
			logger.Warn("skipping Keycloak user",
				"reason", "duplicate email",
				"username", user.Username,
				"email", user.Email,
			)
			scan.skippedUsers++
			scan.ambiguousEmails[email] = true
			continue
		}
		mappings, err := keycloak.effectiveClientRoles(ctx, user.ID, rolesUUID)
		if err != nil {
			return roleHolderScan{}, fmt.Errorf("reading Client Roles of user %q: %w", user.Username, err)
		}
		for _, role := range mappings {
			scan.holders[role.ID] = append(scan.holders[role.ID], user)
		}
	}
	return scan, nil
}

func resolveAccounts(ctx context.Context, outline *outlineClient, roles []clientRole, scan roleHolderScan) (map[string]outlineUser, error) {
	seen := map[string]bool{}
	var emails []string
	for _, role := range roles {
		for _, holder := range scan.holders[role.ID] {
			email := normalizeEmail(holder.Email)
			if !seen[email] {
				seen[email] = true
				emails = append(emails, email)
			}
		}
	}
	for email := range scan.ambiguousEmails {
		if !seen[email] {
			seen[email] = true
			emails = append(emails, email)
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

func planDesiredMembers(
	roles []clientRole,
	scan roleHolderScan,
	accounts map[string]outlineUser,
	logger *slog.Logger,
	summary *runSummary,
) (map[string][]outlineUser, map[string]bool) {
	warnedNoAccount := map[string]bool{}
	desiredByRole := map[string][]outlineUser{}
	for _, role := range roles {
		seen := map[string]bool{}
		for _, holder := range scan.holders[role.ID] {
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
				desiredByRole[role.ID] = append(desiredByRole[role.ID], account)
			}
		}
	}

	protectedAccounts := map[string]bool{}
	for _, account := range accounts {
		if scan.ambiguousEmails[normalizeEmail(account.Email)] {
			protectedAccounts[account.ID] = true
		}
	}
	return desiredByRole, protectedAccounts
}

func readMemberships(ctx context.Context, outline *outlineClient, actions []groupAction) (map[string][]outlineUser, error) {
	memberships := map[string][]outlineUser{}
	for _, action := range actions {
		if action.kind == groupActionCreate {
			continue
		}
		members, err := outline.groupMembers(ctx, action.groupID)
		if err != nil {
			return nil, fmt.Errorf("listing members of Outline Group %q: %w", action.name, err)
		}
		memberships[action.groupID] = members
	}
	return memberships, nil
}

func applyPlan(
	ctx context.Context,
	outline *outlineClient,
	actions []groupAction,
	roles []clientRole,
	memberships map[string][]outlineUser,
	desiredByRole map[string][]outlineUser,
	protectedAccounts map[string]bool,
	logger *slog.Logger,
	summary *runSummary,
	dryRun bool,
) []error {
	var failures []error

	groupsByRole := map[string]outlineGroup{}
	for _, action := range actions {
		group, err := applyGroupAction(ctx, outline, action, logger, summary, dryRun)
		if err != nil {
			failures = append(failures, err)
			if action.kind == groupActionCreate {
				continue
			}
			group = outlineGroup{ID: action.groupID, Name: action.name, ExternalID: action.externalID}
		}
		groupsByRole[action.roleID] = group
	}

	for _, role := range roles {
		group, applied := groupsByRole[role.ID]
		if !applied {
			continue
		}
		if err := reconcileMembers(ctx, outline, group, memberships[group.ID], desiredByRole[role.ID], protectedAccounts, logger, summary, dryRun); err != nil {
			failures = append(failures, err)
		}
	}
	return failures
}

func applyGroupAction(
	ctx context.Context,
	outline *outlineClient,
	action groupAction,
	logger *slog.Logger,
	summary *runSummary,
	dryRun bool,
) (outlineGroup, error) {
	switch action.kind {
	case groupActionCreate:
		if dryRun {
			logger.Info("dry run: would create Managed Group", "group", action.name)
			summary.groupsCreated++
			return outlineGroup{Name: action.name, ExternalID: action.externalID}, nil
		}
		group, err := outline.createGroup(ctx, action.name, action.externalID)
		if err != nil {
			return outlineGroup{}, fmt.Errorf("creating Managed Group %q: %w", action.name, err)
		}
		summary.groupsCreated++
		return group, nil
	case groupActionAdopt:
		if dryRun {
			logger.Info("dry run: would adopt Outline Group",
				"group", action.name,
				"groupId", action.groupID,
				"externalId", action.externalID,
			)
			summary.groupsAdopted++
			return outlineGroup{ID: action.groupID, Name: action.name, ExternalID: action.externalID}, nil
		}
		group, err := outline.updateGroup(ctx, action.groupID, action.name, action.externalID)
		if err != nil {
			return outlineGroup{}, fmt.Errorf("adopting Outline Group as %q: %w", action.name, err)
		}
		summary.groupsAdopted++
		return group, nil
	case groupActionRename:
		if dryRun {
			logger.Info("dry run: would rename Managed Group",
				"group", action.name,
				"groupId", action.groupID,
			)
			summary.groupsRenamed++
			return outlineGroup{ID: action.groupID, Name: action.name, ExternalID: action.externalID}, nil
		}
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

func reconcileMembers(
	ctx context.Context,
	outline *outlineClient,
	group outlineGroup,
	current []outlineUser,
	desired []outlineUser,
	protectedAccounts map[string]bool,
	logger *slog.Logger,
	summary *runSummary,
	dryRun bool,
) error {
	currentMembers := map[string]bool{}
	for _, account := range current {
		currentMembers[account.ID] = true
	}
	desiredMembers := map[string]bool{}
	for _, account := range desired {
		desiredMembers[account.ID] = true
	}

	var failures []error
	for _, account := range current {
		if desiredMembers[account.ID] || protectedAccounts[account.ID] {
			continue
		}
		if dryRun {
			logger.Info("dry run: would remove member",
				"group", group.Name,
				"account", accountLabel(account),
				"accountId", account.ID,
			)
			summary.membersRemoved++
			continue
		}
		if err := outline.removeUserFromGroup(ctx, group.ID, account.ID); err != nil {
			failures = append(failures, fmt.Errorf("removing %s from Outline Group %q: %w", accountLabel(account), group.Name, err))
			continue
		}
		summary.membersRemoved++
	}
	for _, account := range desired {
		if currentMembers[account.ID] {
			continue
		}
		if dryRun {
			logger.Info("dry run: would add member",
				"group", group.Name,
				"account", accountLabel(account),
				"accountId", account.ID,
			)
			summary.membersAdded++
			continue
		}
		if err := outline.addUserToGroup(ctx, group.ID, account.ID); err != nil {
			failures = append(failures, fmt.Errorf("adding %s to Outline Group %q: %w", accountLabel(account), group.Name, err))
			continue
		}
		summary.membersAdded++
	}
	return errors.Join(failures...)
}

func accountLabel(account outlineUser) string {
	if account.Email != "" {
		return account.Email
	}
	return account.ID
}

func normalizeEmail(email string) string {
	return strings.ToLower(email)
}
