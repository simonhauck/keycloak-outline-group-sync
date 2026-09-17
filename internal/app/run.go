package app

import (
	"context"
	"fmt"
	"log/slog"
)

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
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

	outline := newOutlineClient(cfg.outlineURL, cfg.outlineToken)
	groups, err := outline.listGroups(ctx)
	if err != nil {
		return fmt.Errorf("listing Outline Groups: %w", err)
	}

	actions := planGroupActions(roles, groups, managedExternalIDPrefix(cfg))
	summary := runSummary{}
	for _, action := range actions {
		if err := applyGroupAction(ctx, outline, action, &summary); err != nil {
			return err
		}
	}

	logger.Info("sync run complete",
		"groupsCreated", summary.groupsCreated,
		"groupsAdopted", summary.groupsAdopted,
		"groupsRenamed", summary.groupsRenamed,
	)
	return nil
}

type runSummary struct {
	groupsCreated int
	groupsAdopted int
	groupsRenamed int
}

func applyGroupAction(ctx context.Context, outline *outlineClient, action groupAction, summary *runSummary) error {
	switch action.kind {
	case groupActionCreate:
		if _, err := outline.createGroup(ctx, action.name, action.externalID); err != nil {
			return fmt.Errorf("creating Managed Group %q: %w", action.name, err)
		}
		summary.groupsCreated++
	case groupActionAdopt:
		if _, err := outline.updateGroup(ctx, action.groupID, action.name, action.externalID); err != nil {
			return fmt.Errorf("adopting Outline Group as %q: %w", action.name, err)
		}
		summary.groupsAdopted++
	case groupActionRename:
		if _, err := outline.updateGroup(ctx, action.groupID, action.name, action.externalID); err != nil {
			return fmt.Errorf("renaming Managed Group to %q: %w", action.name, err)
		}
		summary.groupsRenamed++
	}
	return nil
}
