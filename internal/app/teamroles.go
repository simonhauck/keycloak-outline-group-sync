package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type teamRole string

const (
	teamRoleAdmin  teamRole = "admin"
	teamRoleMember teamRole = "member"
	teamRoleViewer teamRole = "viewer"
	teamRoleGuest  teamRole = "guest"
)

var teamRolesByRank = []teamRole{teamRoleAdmin, teamRoleMember, teamRoleViewer, teamRoleGuest}

func (r teamRole) rank() int {
	for index, role := range teamRolesByRank {
		if role == r {
			return len(teamRolesByRank) - index
		}
	}
	return -1
}

var fallbackTeamRoles = map[string]teamRole{
	"guest":  teamRoleGuest,
	"viewer": teamRoleViewer,
	"member": teamRoleMember,
}

type teamRoleSignal struct {
	role teamRole
	name string
}

var teamRoleSettings = []struct {
	role teamRole
	env  string
}{
	{teamRoleAdmin, "TEAM_ROLE_ADMIN"},
	{teamRoleMember, "TEAM_ROLE_MEMBER"},
	{teamRoleViewer, "TEAM_ROLE_VIEWER"},
	{teamRoleGuest, "TEAM_ROLE_GUEST"},
}

func loadTeamRoleSignals() ([]teamRoleSignal, error) {
	var signals []teamRoleSignal
	seen := map[string]string{}
	for _, setting := range teamRoleSettings {
		for _, name := range splitList(os.Getenv(setting.env)) {
			if previous, duplicate := seen[name]; duplicate {
				return nil, fmt.Errorf("invalid %s: role %q is already mapped via %s", setting.env, name, previous)
			}
			seen[name] = setting.env
			signals = append(signals, teamRoleSignal{role: setting.role, name: name})
		}
	}
	return signals, nil
}

func loadFallbackTeamRole() (teamRole, error) {
	raw := strings.TrimSpace(os.Getenv("TEAM_ROLE_FALLBACK"))
	if raw == "" {
		return teamRoleViewer, nil
	}
	if role, valid := fallbackTeamRoles[strings.ToLower(raw)]; valid {
		return role, nil
	}
	return "", fmt.Errorf("invalid TEAM_ROLE_FALLBACK: %q must be one of guest, viewer, member", raw)
}

func splitList(raw string) []string {
	var values []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

func resolveRoleSignals(roles []clientRole, signals []teamRoleSignal, rolesClientID string) (map[string]teamRole, error) {
	byName := map[string]clientRole{}
	for _, role := range roles {
		byName[role.Name] = role
	}
	resolved := map[string]teamRole{}
	for _, signal := range signals {
		role, found := byName[signal.name]
		if !found {
			return nil, fmt.Errorf("Role Signal %q does not exist on Keycloak client %q", signal.name, rolesClientID)
		}
		resolved[role.ID] = signal.role
	}
	return resolved, nil
}

func groupMappingRoles(roles []clientRole, signalsByRoleID map[string]teamRole) []clientRole {
	var mapped []clientRole
	for _, role := range roles {
		if _, isSignal := signalsByRoleID[role.ID]; !isSignal {
			mapped = append(mapped, role)
		}
	}
	return mapped
}

type teamRoleAction struct {
	userID string
	email  string
	from   string
	to     teamRole
}

func desiredTeamRole(heldRoleIDs map[string]bool, signalsByRoleID map[string]teamRole, fallback teamRole) teamRole {
	best := fallback
	matched := false
	for roleID := range heldRoleIDs {
		role, found := signalsByRoleID[roleID]
		if !found {
			continue
		}
		if !matched || role.rank() > best.rank() {
			best = role
			matched = true
		}
	}
	return best
}

func planTeamRoles(
	accounts []outlineUser,
	scan roleHolderScan,
	signalsByRoleID map[string]teamRole,
	fallback teamRole,
	tokenOwnerID string,
	logger *slog.Logger,
) []teamRoleAction {
	var actions []teamRoleAction
	for _, account := range accounts {
		email := normalizeEmail(account.Email)
		if email == "" {
			continue
		}
		candidates := scan.enabledByEmail[email]
		var desired teamRole
		switch {
		case len(candidates) > 1:
			continue
		case len(candidates) == 1:
			desired = desiredTeamRole(scan.userRoles[candidates[0].ID], signalsByRoleID, fallback)
		case scan.disabledEmails[email]:
			desired = fallback
		default:
			continue
		}
		if account.ID == tokenOwnerID {
			if string(desired) != account.Role {
				logger.Warn("skipping Outline Account",
					"reason", "token owner",
					"account", accountLabel(account),
					"accountId", account.ID,
				)
			}
			continue
		}
		if string(desired) == account.Role {
			continue
		}
		actions = append(actions, teamRoleAction{
			userID: account.ID,
			email:  account.Email,
			from:   account.Role,
			to:     desired,
		})
	}
	return actions
}

func applyTeamRolePlan(
	ctx context.Context,
	outline *outlineClient,
	actions []teamRoleAction,
	logger *slog.Logger,
	summary *runSummary,
	dryRun bool,
) []error {
	var failures []error
	for _, action := range actions {
		if dryRun {
			logger.Info("dry run: would change Team Role",
				"account", action.email,
				"accountId", action.userID,
				"from", action.from,
				"to", action.to,
			)
			summary.teamRolesChanged++
			continue
		}
		if err := outline.updateUserRole(ctx, action.userID, action.to); err != nil {
			failures = append(failures, fmt.Errorf("changing Team Role of %s to %s: %w", action.email, action.to, err))
			continue
		}
		summary.teamRolesChanged++
	}
	return failures
}
