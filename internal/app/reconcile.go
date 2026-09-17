package app

import (
	"fmt"
	"strings"
)

type groupActionKind int

const (
	groupActionKeep groupActionKind = iota
	groupActionCreate
	groupActionAdopt
	groupActionRename
)

type groupAction struct {
	kind       groupActionKind
	roleID     string
	groupID    string
	name       string
	externalID string
}

func managedExternalIDPrefix(cfg config) string {
	return fmt.Sprintf("keycloak:%s:%s:", cfg.keycloakRealm, cfg.rolesClientID)
}

func managedRoleID(externalID, externalIDPrefix string) (string, bool) {
	if !strings.HasPrefix(externalID, externalIDPrefix) {
		return "", false
	}
	return strings.TrimPrefix(externalID, externalIDPrefix), true
}

func findOrphanedGroups(groups []outlineGroup, roles []clientRole, externalIDPrefix string) []outlineGroup {
	roleIDs := map[string]bool{}
	for _, role := range roles {
		roleIDs[role.ID] = true
	}

	var orphaned []outlineGroup
	for _, group := range groups {
		roleID, managed := managedRoleID(group.ExternalID, externalIDPrefix)
		if !managed || roleIDs[roleID] {
			continue
		}
		orphaned = append(orphaned, group)
	}
	return orphaned
}

func planGroupActions(roles []clientRole, groups []outlineGroup, externalIDPrefix string) []groupAction {
	var actions []groupAction
	for _, role := range roles {
		externalID := externalIDPrefix + role.ID

		if existing, found := findGroupByExternalID(groups, externalID); found {
			kind := groupActionKeep
			if existing.Name != role.Name {
				kind = groupActionRename
			}
			actions = append(actions, groupAction{
				kind:       kind,
				roleID:     role.ID,
				groupID:    existing.ID,
				name:       role.Name,
				externalID: externalID,
			})
			continue
		}

		if existing, found := findAdoptableGroupByName(groups, role.Name); found {
			actions = append(actions, groupAction{
				kind:       groupActionAdopt,
				roleID:     role.ID,
				groupID:    existing.ID,
				name:       role.Name,
				externalID: externalID,
			})
			continue
		}

		actions = append(actions, groupAction{
			kind:       groupActionCreate,
			roleID:     role.ID,
			name:       role.Name,
			externalID: externalID,
		})
	}
	return actions
}

func findGroupByExternalID(groups []outlineGroup, externalID string) (outlineGroup, bool) {
	for _, group := range groups {
		if group.ExternalID == externalID {
			return group, true
		}
	}
	return outlineGroup{}, false
}

func findAdoptableGroupByName(groups []outlineGroup, name string) (outlineGroup, bool) {
	for _, group := range groups {
		if group.ExternalID == "" && strings.EqualFold(group.Name, name) {
			return group, true
		}
	}
	return outlineGroup{}, false
}
