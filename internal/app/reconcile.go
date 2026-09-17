package app

import (
	"fmt"
	"strings"
)

type groupActionKind int

const (
	groupActionCreate groupActionKind = iota
	groupActionAdopt
	groupActionRename
)

type groupAction struct {
	kind       groupActionKind
	groupID    string
	name       string
	externalID string
}

func managedExternalIDPrefix(cfg config) string {
	return fmt.Sprintf("keycloak:%s:%s:", cfg.keycloakRealm, cfg.rolesClientID)
}

func planGroupActions(roles []keycloakRole, groups []outlineGroup, externalIDPrefix string) []groupAction {
	var actions []groupAction
	for _, role := range roles {
		externalID := externalIDPrefix + role.ID

		if existing, found := findGroupByExternalID(groups, externalID); found {
			if existing.Name != role.Name {
				actions = append(actions, groupAction{
					kind:       groupActionRename,
					groupID:    existing.ID,
					name:       role.Name,
					externalID: externalID,
				})
			}
			continue
		}

		if existing, found := findAdoptableGroupByName(groups, role.Name); found {
			actions = append(actions, groupAction{
				kind:       groupActionAdopt,
				groupID:    existing.ID,
				name:       role.Name,
				externalID: externalID,
			})
			continue
		}

		actions = append(actions, groupAction{
			kind:       groupActionCreate,
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
