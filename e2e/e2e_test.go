//go:build e2e

// Package e2e runs the Sync Service against real Keycloak and Outline
// containers started by docker compose. The test owns the stack lifecycle and
// removes its volumes on exit. Run it with:
//
//	go test -tags e2e -count=1 ./e2e/
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const (
	composeFile = "docker-compose.yml"
	outlineURL  = "http://127.0.0.1:13000"
	keycloakURL = "http://127.0.0.1:18080"
	realm       = "e2e"
	rolesClient = "roles-client"
)

type stack struct {
	apiKey   string
	kcToken  string
	adoptID  string
	handbook string
	accounts map[string]string
}

var httpClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func TestSyncServiceAgainstRealDependencies(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	s := startStack(t)

	t.Run("first run creates adopts and adds members", func(t *testing.T) {
		s.mustRunSync(t, nil)

		for _, name := range []string{
			"team-create", "team-adopt", "team-members", "team-direct",
			"team-disabled", "team-duplicate", "team-noaccount", "team-rename",
		} {
			s.assertManaged(t, name)
		}
		if got := s.findGroup(t, "team-adopt").ID; got != s.adoptID {
			t.Fatalf("team-adopt was not adopted: group id is %s, want the pre-existing %s", got, s.adoptID)
		}
		s.assertMembers(t, "team-create")
		s.assertMembers(t, "team-members", "alice@example.com")
		s.assertMembers(t, "team-direct", "bob@example.com")
		s.assertMembers(t, "team-disabled", "carol@example.com")
		s.assertMembers(t, "team-duplicate")
		s.assertMembers(t, "team-noaccount")
		s.assertMembers(t, "handbook", "frank@example.com")
		if got := s.findGroup(t, "handbook"); got.ExternalID != "" || got.ID != s.handbook {
			t.Fatalf("unmanaged group handbook was touched: %+v", got)
		}
	})

	t.Run("second run is a no-op", func(t *testing.T) {
		before := s.snapshot(t)
		output := s.mustRunSync(t, nil)
		after := s.snapshot(t)
		if before != after {
			t.Fatalf("a second Sync Run changed Outline state:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		for _, want := range []string{
			`"groupsCreated":0`, `"groupsAdopted":0`, `"groupsRenamed":0`,
			`"membersAdded":0`, `"membersRemoved":0`, `"failures":0`,
		} {
			if !strings.Contains(output, want) {
				t.Fatalf("no-op run summary does not contain %s:\n%s", want, output)
			}
		}
	})

	t.Run("removes members disables users and renames roles", func(t *testing.T) {
		s.removeFromKeycloakGroup(t, "alice", "g-members")
		s.disableKeycloakUser(t, "carol")
		s.renameClientRole(t, "team-rename", "team-renamed")

		s.mustRunSync(t, nil)

		s.assertMembers(t, "team-members")
		s.assertMembers(t, "team-disabled")
		renamed := s.findGroup(t, "team-renamed")
		if !strings.HasPrefix(renamed.ExternalID, "keycloak:"+realm+":"+rolesClient+":") {
			t.Fatalf("team-renamed lost its external id: %+v", renamed)
		}
		for _, group := range s.groups(t) {
			if group.Name == "team-rename" {
				t.Fatalf("the old group name still exists: %+v", group)
			}
		}
	})

	t.Run("failed keycloak fetch writes nothing", func(t *testing.T) {
		before := s.snapshot(t)
		if output, err := s.runSync(t, map[string]string{"KEYCLOAK_URL": "http://127.0.0.1:9"}); err == nil {
			t.Fatalf("expected a non-zero exit when Keycloak is unreachable\n%s", output)
		}
		if after := s.snapshot(t); before != after {
			t.Fatalf("a failed Keycloak fetch changed Outline state:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

func startStack(t *testing.T) *stack {
	t.Helper()
	s := &stack{}
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := s.compose("logs", "--no-color", "keycloak", "outline"); err == nil {
				t.Logf("compose logs:\n%s", tail(logs, 20000))
			}
		}
		if out, err := s.compose("down", "-v", "--remove-orphans"); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	if out, err := s.compose("up", "-d", "--wait", "outline"); err != nil {
		t.Fatalf("starting the compose stack: %v\n%s", err, out)
	}
	s.apiKey = s.bootstrapOutline(t)
	s.kcToken = s.keycloakToken(t)
	s.seedOutline(t)
	return s
}

func (s *stack) compose(args ...string) (string, error) {
	command := exec.Command("docker", append([]string{"compose", "-f", composeFile}, args...)...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), err
}

func (s *stack) mustCompose(t *testing.T, args ...string) string {
	t.Helper()
	output, err := s.compose(args...)
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, output)
	}
	return output
}

func (s *stack) runSync(t *testing.T, env map[string]string) (string, error) {
	t.Helper()
	effective := map[string]string{"OUTLINE_TOKEN": s.apiKey}
	for key, value := range env {
		effective[key] = value
	}
	args := []string{"run", "--rm"}
	for _, key := range sortedKeys(effective) {
		args = append(args, "-e", key+"="+effective[key])
	}
	args = append(args, "sync", "--once")
	return s.compose(args...)
}

func (s *stack) mustRunSync(t *testing.T, env map[string]string) string {
	t.Helper()
	output, err := s.runSync(t, env)
	if err != nil {
		t.Fatalf("Sync Run failed: %v\n%s", err, output)
	}
	return output
}

func (s *stack) bootstrapOutline(t *testing.T) string {
	t.Helper()
	resp := doRequest(t, http.MethodPost, outlineURL+"/api/installation.create", "", map[string]any{
		"teamName":  "E2E Team",
		"userName":  "E2E Admin",
		"userEmail": "admin@example.com",
	})
	if resp.status != http.StatusOK && resp.status != http.StatusFound {
		t.Fatalf("Outline installation.create returned %d: %s", resp.status, resp.body)
	}
	var accessToken string
	for _, cookie := range resp.cookies {
		if cookie.Name == "accessToken" {
			accessToken = cookie.Value
		}
	}
	if accessToken == "" {
		t.Fatalf("Outline installation.create did not set an accessToken cookie")
	}

	resp = doRequest(t, http.MethodPost, outlineURL+"/api/apiKeys.create", accessToken, map[string]any{"name": "e2e"})
	if resp.status != http.StatusOK {
		t.Fatalf("Outline apiKeys.create returned %d: %s", resp.status, resp.body)
	}
	var created struct {
		Data struct {
			Value string `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.body, &created); err != nil || created.Data.Value == "" {
		t.Fatalf("Outline apiKeys.create returned no key: %s (%v)", resp.body, err)
	}
	return created.Data.Value
}

func (s *stack) keycloakToken(t *testing.T) string {
	t.Helper()
	resp := doFormRequest(t, keycloakURL+"/realms/master/protocol/openid-connect/token", url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {"admin"},
		"password":   {"admin"},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("Keycloak admin token request returned %d: %s", resp.status, resp.body)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(resp.body, &token); err != nil || token.AccessToken == "" {
		t.Fatalf("Keycloak admin token request returned no token: %s (%v)", resp.body, err)
	}
	return token.AccessToken
}

func (s *stack) seedOutline(t *testing.T) {
	t.Helper()
	var invited struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	s.outline(t, "users.invite", map[string]any{
		"invites": []map[string]any{
			{"email": "alice@example.com", "name": "Alice", "role": "member"},
			{"email": "bob@example.com", "name": "Bob", "role": "member"},
			{"email": "carol@example.com", "name": "Carol", "role": "member"},
			{"email": "dave@example.com", "name": "Dave", "role": "member"},
			{"email": "frank@example.com", "name": "Frank", "role": "member"},
		},
		"suppressEmail": true,
	}, &invited)

	var adopt struct {
		ID string `json:"id"`
	}
	s.outline(t, "groups.create", map[string]any{"name": "team-adopt"}, &adopt)
	s.adoptID = adopt.ID

	var handbook struct {
		ID string `json:"id"`
	}
	s.outline(t, "groups.create", map[string]any{"name": "handbook"}, &handbook)
	s.handbook = handbook.ID

	s.accounts = map[string]string{}
	var frankID string
	for _, user := range invited.Users {
		s.accounts[user.ID] = user.Email
		if user.Email == "frank@example.com" {
			frankID = user.ID
		}
	}
	if frankID == "" {
		t.Fatalf("frank was not invited: %+v", invited.Users)
	}
	s.outline(t, "groups.add_user", map[string]any{"id": s.handbook, "userId": frankID}, nil)
}

func (s *stack) outline(t *testing.T, route string, payload any, out any) {
	t.Helper()
	resp := doRequest(t, http.MethodPost, outlineURL+"/api/"+route, s.apiKey, payload)
	if resp.status != http.StatusOK {
		t.Fatalf("Outline %s returned %d: %s", route, resp.status, resp.body)
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.body, &envelope); err != nil {
		t.Fatalf("Outline %s returned unreadable data: %s (%v)", route, resp.body, err)
	}
	if !envelope.OK {
		t.Fatalf("Outline %s failed: %s", route, envelope.Error)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			t.Fatalf("Outline %s returned unexpected data: %s (%v)", route, envelope.Data, err)
		}
	}
}

func (s *stack) keycloak(t *testing.T, method, path string, payload any, out any) {
	t.Helper()
	resp := doRequest(t, method, keycloakURL+"/admin/realms/"+realm+path, s.kcToken, payload)
	if resp.status < 200 || resp.status > 299 {
		t.Fatalf("Keycloak %s %s returned %d: %s", method, path, resp.status, resp.body)
	}
	if out != nil {
		if err := json.Unmarshal(resp.body, out); err != nil {
			t.Fatalf("Keycloak %s %s returned unexpected data: %s (%v)", method, path, resp.body, err)
		}
	}
}

type group struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExternalID string `json:"externalId"`
}

func (s *stack) groups(t *testing.T) []group {
	t.Helper()
	var all []group
	for offset := 0; ; {
		var data struct {
			Groups []group `json:"groups"`
		}
		s.outline(t, "groups.list", map[string]any{"limit": 100, "offset": offset}, &data)
		all = append(all, data.Groups...)
		if len(data.Groups) == 0 {
			return all
		}
		offset += len(data.Groups)
	}
}

func (s *stack) findGroup(t *testing.T, name string) group {
	t.Helper()
	for _, candidate := range s.groups(t) {
		if candidate.Name == name {
			return candidate
		}
	}
	var names []string
	for _, candidate := range s.groups(t) {
		names = append(names, candidate.Name)
	}
	t.Fatalf("no Outline Group named %q; have %v", name, names)
	return group{}
}

func (s *stack) members(t *testing.T, groupID string) []string {
	t.Helper()
	var emails []string
	for offset := 0; ; {
		var data struct {
			GroupMemberships []struct {
				UserID string `json:"userId"`
			} `json:"groupMemberships"`
		}
		s.outline(t, "groups.memberships", map[string]any{"id": groupID, "limit": 100, "offset": offset}, &data)
		for _, membership := range data.GroupMemberships {
			if email, found := s.accounts[membership.UserID]; found {
				emails = append(emails, email)
			} else {
				emails = append(emails, membership.UserID)
			}
		}
		if len(data.GroupMemberships) == 0 {
			sort.Strings(emails)
			return emails
		}
		offset += len(data.GroupMemberships)
	}
}

func (s *stack) assertMembers(t *testing.T, groupName string, want ...string) {
	t.Helper()
	got := s.members(t, s.findGroup(t, groupName).ID)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("members of %q:\n got: %v\nwant: %v", groupName, got, want)
	}
}

func (s *stack) assertManaged(t *testing.T, groupName string) {
	t.Helper()
	group := s.findGroup(t, groupName)
	prefix := "keycloak:" + realm + ":" + rolesClient + ":"
	if !strings.HasPrefix(group.ExternalID, prefix) {
		t.Fatalf("Outline Group %q is not managed: externalId %q does not start with %q", groupName, group.ExternalID, prefix)
	}
}

func (s *stack) snapshot(t *testing.T) string {
	t.Helper()
	var lines []string
	for _, group := range s.groups(t) {
		lines = append(lines, fmt.Sprintf("%s|%s|%s",
			group.Name, group.ExternalID, strings.Join(s.members(t, group.ID), ",")))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (s *stack) removeFromKeycloakGroup(t *testing.T, username, groupName string) {
	t.Helper()
	user := s.keycloakUser(t, username)
	group := s.keycloakGroup(t, groupName)
	s.keycloak(t, http.MethodDelete, "/users/"+user.ID+"/groups/"+group.ID, nil, nil)
}

func (s *stack) disableKeycloakUser(t *testing.T, username string) {
	t.Helper()
	user := s.keycloakUser(t, username)
	var representation map[string]any
	s.keycloak(t, http.MethodGet, "/users/"+user.ID, nil, &representation)
	representation["enabled"] = false
	s.keycloak(t, http.MethodPut, "/users/"+user.ID, representation, nil)
}

func (s *stack) renameClientRole(t *testing.T, from, to string) {
	t.Helper()
	var clients []struct {
		ID       string `json:"id"`
		ClientID string `json:"clientId"`
	}
	s.keycloak(t, http.MethodGet, "/clients?clientId="+url.QueryEscape(rolesClient), nil, &clients)
	if len(clients) != 1 {
		t.Fatalf("expected exactly one %q client, got %+v", rolesClient, clients)
	}
	s.keycloak(t, http.MethodPut, "/clients/"+clients[0].ID+"/roles/"+url.PathEscape(from),
		map[string]any{"name": to}, nil)
}

type keycloakRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *stack) keycloakUser(t *testing.T, username string) keycloakRef {
	t.Helper()
	var users []keycloakRef
	s.keycloak(t, http.MethodGet, "/users?username="+url.QueryEscape(username)+"&exact=true", nil, &users)
	if len(users) != 1 {
		t.Fatalf("expected exactly one Keycloak user %q, got %+v", username, users)
	}
	return users[0]
}

func (s *stack) keycloakGroup(t *testing.T, name string) keycloakRef {
	t.Helper()
	var groups []keycloakRef
	s.keycloak(t, http.MethodGet, "/groups?search="+url.QueryEscape(name), nil, &groups)
	for _, candidate := range groups {
		if candidate.ID != "" && candidate.Name == name {
			return candidate
		}
	}
	t.Fatalf("no Keycloak group named %q in %+v", name, groups)
	return keycloakRef{}
}

type httpResponse struct {
	status  int
	body    []byte
	cookies []*http.Cookie
}

func doRequest(t *testing.T, method, rawURL, token string, payload any) httpResponse {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encoding request body: %v", err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return executeRequest(t, request)
}

func doFormRequest(t *testing.T, rawURL string, form url.Values) httpResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return executeRequest(t, request)
}

func executeRequest(t *testing.T, request *http.Request) httpResponse {
	t.Helper()
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", request.Method, request.URL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading %s %s: %v", request.Method, request.URL, err)
	}
	return httpResponse{status: response.StatusCode, body: body, cookies: response.Cookies()}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func tail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
