package app_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testRealm        = "test"
	testClientID     = "sync-client"
	testClientSecret = "sync-secret"
	testRolesClient  = "roles-client"
	testRolesUUID    = "roles-client-uuid"
	testAccessToken  = "test-access-token"
	testOutlineToken = "test-outline-token"
)

type recordedRequest struct {
	Method        string
	Path          string
	Query         url.Values
	Form          url.Values
	JSON          map[string]any
	Authorization string
}

type clientRole struct {
	ID   string
	Name string
}

type keycloakClientRep struct {
	ID       string
	ClientID string
}

type keycloakUser struct {
	ID          string
	Username    string
	Email       string
	Enabled     bool
	DirectRoles []string
}

type keycloakGroup struct {
	ID       string
	ParentID string
	Roles    []string
	Members  []string
}

type fakeKeycloak struct {
	realm         string
	clients       []keycloakClientRep
	roles         []clientRole
	users         []keycloakUser
	groups        []keycloakGroup
	pageSize      int
	failUsers     int
	failComposite int

	mu       sync.Mutex
	requests []recordedRequest
	server   *httptest.Server
}

func newFakeKeycloak(t *testing.T, roles []clientRole) *fakeKeycloak {
	t.Helper()
	f := &fakeKeycloak{
		realm:   testRealm,
		clients: []keycloakClientRep{{ID: testRolesUUID, ClientID: testRolesClient}},
		roles:   roles,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeKeycloak) URL() string { return f.server.URL }

func (f *fakeKeycloak) seedUsers(users ...keycloakUser) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, users...)
}

func (f *fakeKeycloak) seedGroups(groups ...keycloakGroup) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups = append(f.groups, groups...)
}

func (f *fakeKeycloak) failNextUserListings(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failUsers = count
}

func (f *fakeKeycloak) failNextRoleMappingListings(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failComposite = count
}

func (f *fakeKeycloak) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	recorded, err := recordRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.requests = append(f.requests, recorded)

	switch {
	case r.URL.Path == "/realms/"+f.realm+"/protocol/openid-connect/token":
		f.handleToken(w, recorded)
	case r.URL.Path == "/admin/realms/"+f.realm+"/clients":
		f.handleClients(w, r, recorded)
	case r.URL.Path == "/admin/realms/"+f.realm+"/users":
		f.handleUsers(w, r, recorded)
	case strings.HasPrefix(r.URL.Path, "/admin/realms/"+f.realm+"/users/") && strings.HasSuffix(r.URL.Path, "/composite"):
		f.handleCompositeRoles(w, r, recorded)
	case strings.HasPrefix(r.URL.Path, "/admin/realms/"+f.realm+"/clients/") && strings.HasSuffix(r.URL.Path, "/roles"):
		f.handleRoles(w, r, recorded)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeKeycloak) handleToken(w http.ResponseWriter, recorded recordedRequest) {
	if recorded.Form.Get("grant_type") != "client_credentials" {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	if recorded.Form.Get("client_id") == "" || recorded.Form.Get("client_secret") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	writeJSON(w, map[string]any{
		"access_token": testAccessToken,
		"expires_in":   300,
		"token_type":   "Bearer",
	})
}

func (f *fakeKeycloak) handleClients(w http.ResponseWriter, r *http.Request, recorded recordedRequest) {
	if !f.authorized(w, recorded) {
		return
	}
	wanted := r.URL.Query().Get("clientId")
	clients := []map[string]string{}
	for _, client := range f.clients {
		if wanted == "" || client.ClientID == wanted {
			clients = append(clients, map[string]string{"id": client.ID, "clientId": client.ClientID})
		}
	}
	writeJSON(w, clients)
}

func (f *fakeKeycloak) handleRoles(w http.ResponseWriter, r *http.Request, recorded recordedRequest) {
	if !f.authorized(w, recorded) {
		return
	}
	clientUUID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin/realms/"+f.realm+"/clients/"), "/roles")
	if clientUUID != testRolesUUID {
		http.NotFound(w, r)
		return
	}

	first := queryInt(r, "first", 0)
	page := f.roles[min(first, len(f.roles)):]
	if f.pageSize > 0 && len(page) > f.pageSize {
		page = page[:f.pageSize]
	}
	roles := []map[string]string{}
	for _, role := range page {
		roles = append(roles, map[string]string{"id": role.ID, "name": role.Name})
	}
	writeJSON(w, roles)
}

func (f *fakeKeycloak) handleUsers(w http.ResponseWriter, r *http.Request, recorded recordedRequest) {
	if !f.authorized(w, recorded) {
		return
	}
	if f.failUsers > 0 {
		f.failUsers--
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": "Internal Server Error"})
		return
	}
	first := queryInt(r, "first", 0)
	page := f.users[min(first, len(f.users)):]
	if f.pageSize > 0 && len(page) > f.pageSize {
		page = page[:f.pageSize]
	}
	users := []map[string]any{}
	for _, user := range page {
		users = append(users, map[string]any{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
			"enabled":  user.Enabled,
		})
	}
	writeJSON(w, users)
}

func (f *fakeKeycloak) handleCompositeRoles(w http.ResponseWriter, r *http.Request, recorded recordedRequest) {
	if !f.authorized(w, recorded) {
		return
	}
	if f.failComposite > 0 {
		f.failComposite--
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": "Internal Server Error"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/realms/"+f.realm+"/users/")
	userID, rest, found := strings.Cut(path, "/")
	if !found || rest != "role-mappings/clients/"+testRolesUUID+"/composite" {
		http.NotFound(w, r)
		return
	}
	user, found := f.userByID(userID)
	if !found {
		http.NotFound(w, r)
		return
	}

	roles := []map[string]string{}
	for _, role := range f.roles {
		if f.userHasRole(user, role.Name) {
			roles = append(roles, map[string]string{"id": role.ID, "name": role.Name})
		}
	}
	writeJSON(w, roles)
}

func (f *fakeKeycloak) userHasRole(user keycloakUser, roleName string) bool {
	for _, direct := range user.DirectRoles {
		if direct == roleName {
			return true
		}
	}
	for _, group := range f.groups {
		if !slices.Contains(group.Members, user.ID) {
			continue
		}
		for id := group.ID; id != ""; {
			current, found := f.groupByID(id)
			if !found {
				break
			}
			if slices.Contains(current.Roles, roleName) {
				return true
			}
			id = current.ParentID
		}
	}
	return false
}

func (f *fakeKeycloak) userByID(id string) (keycloakUser, bool) {
	for _, user := range f.users {
		if user.ID == id {
			return user, true
		}
	}
	return keycloakUser{}, false
}

func (f *fakeKeycloak) groupByID(id string) (keycloakGroup, bool) {
	for _, group := range f.groups {
		if group.ID == id {
			return group, true
		}
	}
	return keycloakGroup{}, false
}

func (f *fakeKeycloak) authorized(w http.ResponseWriter, recorded recordedRequest) bool {
	if recorded.Authorization != "Bearer "+testAccessToken {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "HTTP 401 Unauthorized"})
		return false
	}
	return true
}

func (f *fakeKeycloak) allRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

type outlineGroup struct {
	ID         string
	Name       string
	ExternalID string
}

type outlineUser struct {
	ID    string
	Email string
}

type fakeOutline struct {
	pageSize        int
	listGroupsDelay time.Duration
	failListGroups  int
	failAddUser     int

	mu          sync.Mutex
	groups      []outlineGroup
	users       []outlineUser
	memberships map[string][]string
	counter     int
	requests    []recordedRequest
	inFlight    int
	maxInFlight int
	server      *httptest.Server
}

func newFakeOutline(t *testing.T) *fakeOutline {
	t.Helper()
	f := &fakeOutline{memberships: map[string][]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOutline) URL() string { return f.server.URL }

func (f *fakeOutline) seedGroups(groups ...outlineGroup) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups = append(f.groups, groups...)
}

func (f *fakeOutline) seedUsers(users ...outlineUser) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, users...)
}

func (f *fakeOutline) seedMembership(groupID string, userIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memberships[groupID] = append(f.memberships[groupID], userIDs...)
}

func (f *fakeOutline) failNextAddUser(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAddUser = count
}

func (f *fakeOutline) handle(w http.ResponseWriter, r *http.Request) {
	recorded, err := recordRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.requests = append(f.requests, recorded)
	f.mu.Unlock()

	if recorded.Authorization != "Bearer "+testOutlineToken {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"ok": false, "error": "Unauthorized"})
		return
	}

	switch r.URL.Path {
	case "/api/groups.list":
		f.handleListGroups(w, recorded)
	case "/api/groups.create":
		f.handleCreateGroup(w, recorded)
	case "/api/groups.update":
		f.handleUpdateGroup(w, recorded)
	case "/api/groups.memberships":
		f.handleGroupMemberships(w, recorded)
	case "/api/groups.add_user":
		f.handleAddUser(w, recorded)
	case "/api/groups.remove_user":
		f.handleRemoveUser(w, recorded)
	case "/api/users.list":
		f.handleListUsers(w, recorded)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOutline) handleListGroups(w http.ResponseWriter, recorded recordedRequest) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	delay := f.listGroupsDelay
	fail := f.failListGroups > 0
	if fail {
		f.failListGroups--
	}

	limit := intValue(recorded.JSON["limit"], 15)
	offset := intValue(recorded.JSON["offset"], 0)
	if limit <= 0 {
		limit = 15
	}
	if f.pageSize > 0 && limit > f.pageSize {
		limit = f.pageSize
	}
	if offset > len(f.groups) {
		offset = len(f.groups)
	}
	end := min(offset+limit, len(f.groups))

	groups := []map[string]any{}
	for _, group := range f.groups[offset:end] {
		groups = append(groups, presentGroup(group))
	}
	total := len(f.groups)
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()

	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"ok": false, "error": "Internal Server Error"})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true,
		"data": map[string]any{
			"groups":           groups,
			"groupMemberships": []any{},
		},
		"pagination": map[string]int{
			"limit":  limit,
			"offset": offset,
			"total":  total,
		},
	})
}

func (f *fakeOutline) handleCreateGroup(w http.ResponseWriter, recorded recordedRequest) {
	name, _ := recorded.JSON["name"].(string)
	externalID, _ := recorded.JSON["externalId"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"ok": false, "error": "name is required"})
		return
	}
	f.mu.Lock()
	f.counter++
	group := outlineGroup{ID: fmt.Sprintf("created-group-%d", f.counter), Name: name, ExternalID: externalID}
	f.groups = append(f.groups, group)
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "data": map[string]any{"group": presentGroup(group)}})
}

func (f *fakeOutline) handleUpdateGroup(w http.ResponseWriter, recorded recordedRequest) {
	id, _ := recorded.JSON["id"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, group := range f.groups {
		if group.ID != id {
			continue
		}
		if name, ok := recorded.JSON["name"].(string); ok {
			group.Name = name
		}
		if externalID, ok := recorded.JSON["externalId"].(string); ok {
			group.ExternalID = externalID
		}
		f.groups[i] = group
		writeJSON(w, map[string]any{"ok": true, "data": map[string]any{"group": presentGroup(group)}})
		return
	}
	w.WriteHeader(http.StatusNotFound)
	writeJSON(w, map[string]any{"ok": false, "error": "group not found"})
}

func (f *fakeOutline) handleGroupMemberships(w http.ResponseWriter, recorded recordedRequest) {
	id, _ := recorded.JSON["id"].(string)
	limit, offset := listWindow(recorded, f.pageSize)

	f.mu.Lock()
	defer f.mu.Unlock()
	members := append([]string(nil), f.memberships[id]...)

	if offset > len(members) {
		offset = len(members)
	}
	end := min(offset+limit, len(members))
	groupMemberships := []map[string]any{}
	pageUsers := []map[string]any{}
	for _, userID := range members[offset:end] {
		groupMemberships = append(groupMemberships, map[string]any{
			"id":         userID + "-" + id,
			"userId":     userID,
			"groupId":    id,
			"permission": "read",
		})
		if user, found := f.userRecord(userID); found {
			pageUsers = append(pageUsers, presentUser(user))
		}
	}
	writeJSON(w, map[string]any{
		"ok": true,
		"data": map[string]any{
			"groupMemberships": groupMemberships,
			"users":            pageUsers,
		},
		"pagination": map[string]int{
			"limit":  limit,
			"offset": offset,
			"total":  len(members),
		},
	})
}

func (f *fakeOutline) handleAddUser(w http.ResponseWriter, recorded recordedRequest) {
	groupID, _ := recorded.JSON["id"].(string)
	userID, _ := recorded.JSON["userId"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAddUser > 0 {
		f.failAddUser--
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"ok": false, "error": "Internal Server Error"})
		return
	}
	group, found := f.groupRecordByID(groupID)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"ok": false, "error": "group not found"})
		return
	}
	user, found := f.userRecord(userID)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"ok": false, "error": "user not found"})
		return
	}
	if !slices.Contains(f.memberships[groupID], userID) {
		f.memberships[groupID] = append(f.memberships[groupID], userID)
	}
	writeJSON(w, map[string]any{
		"ok": true,
		"data": map[string]any{
			"users": []map[string]any{presentUser(user)},
			"groupMemberships": []map[string]any{{
				"id":         userID + "-" + groupID,
				"userId":     userID,
				"groupId":    groupID,
				"permission": "read",
			}},
			"groups": []map[string]any{presentGroup(group)},
		},
	})
}

func (f *fakeOutline) handleRemoveUser(w http.ResponseWriter, recorded recordedRequest) {
	groupID, _ := recorded.JSON["id"].(string)
	userID, _ := recorded.JSON["userId"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	group, found := f.groupRecordByID(groupID)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"ok": false, "error": "group not found"})
		return
	}
	if _, found := f.userRecord(userID); !found {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"ok": false, "error": "user not found"})
		return
	}
	members := f.memberships[groupID]
	for i, memberID := range members {
		if memberID == userID {
			f.memberships[groupID] = append(members[:i], members[i+1:]...)
			break
		}
	}
	writeJSON(w, map[string]any{
		"ok":   true,
		"data": map[string]any{"groups": []map[string]any{presentGroup(group)}},
	})
}

func (f *fakeOutline) handleListUsers(w http.ResponseWriter, recorded recordedRequest) {
	wanted := map[string]bool{}
	if emails, ok := recorded.JSON["emails"].([]any); ok {
		for _, email := range emails {
			if value, ok := email.(string); ok {
				wanted[value] = true
			}
		}
	}
	limit, offset := listWindow(recorded, f.pageSize)

	f.mu.Lock()
	matched := []outlineUser{}
	for _, user := range f.users {
		if wanted[user.Email] {
			matched = append(matched, user)
		}
	}
	f.mu.Unlock()

	if offset > len(matched) {
		offset = len(matched)
	}
	end := min(offset+limit, len(matched))
	users := []map[string]any{}
	for _, user := range matched[offset:end] {
		users = append(users, presentUser(user))
	}
	writeJSON(w, map[string]any{
		"ok":         true,
		"data":       users,
		"pagination": map[string]int{"limit": limit, "offset": offset, "total": len(matched)},
	})
}

func (f *fakeOutline) groupRecordByID(id string) (outlineGroup, bool) {
	for _, group := range f.groups {
		if group.ID == id {
			return group, true
		}
	}
	return outlineGroup{}, false
}

func (f *fakeOutline) userRecord(id string) (outlineUser, bool) {
	for _, user := range f.users {
		if user.ID == id {
			return user, true
		}
	}
	return outlineUser{}, false
}

func listWindow(recorded recordedRequest, pageCap int) (limit, offset int) {
	limit = intValue(recorded.JSON["limit"], 15)
	offset = intValue(recorded.JSON["offset"], 0)
	if limit <= 0 {
		limit = 15
	}
	if pageCap > 0 && limit > pageCap {
		limit = pageCap
	}
	return limit, offset
}

func (f *fakeOutline) snapshot() []outlineGroup {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]outlineGroup(nil), f.groups...)
}

func (f *fakeOutline) allRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeOutline) failNextListGroups(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failListGroups = count
}

func (f *fakeOutline) maxListInFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInFlight
}

func (f *fakeOutline) listCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls int
	for _, request := range f.requests {
		if request.Path == "/api/groups.list" {
			calls++
		}
	}
	return calls
}

func (f *fakeOutline) groupMembers(groupID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.memberships[groupID]...)
}

func (f *fakeOutline) writeRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var writes []recordedRequest
	for _, request := range f.requests {
		switch request.Path {
		case "/api/groups.create", "/api/groups.update", "/api/groups.add_user", "/api/groups.remove_user":
			writes = append(writes, request)
		}
	}
	return writes
}

func presentGroup(group outlineGroup) map[string]any {
	return map[string]any{
		"id":          group.ID,
		"name":        group.Name,
		"externalId":  group.ExternalID,
		"memberCount": 0,
	}
}

func presentUser(user outlineUser) map[string]any {
	return map[string]any{
		"id":    user.ID,
		"name":  user.ID,
		"email": user.Email,
	}
}

func recordRequest(r *http.Request) (recordedRequest, error) {
	recorded := recordedRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         r.URL.Query(),
		Authorization: r.Header.Get("Authorization"),
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return recorded, err
	}
	if len(body) == 0 {
		return recorded, nil
	}
	contentType := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		recorded.Form, err = url.ParseQuery(string(body))
		if err != nil {
			return recorded, err
		}
	case strings.HasPrefix(contentType, "application/json"):
		if err := json.Unmarshal(body, &recorded.JSON); err != nil {
			return recorded, err
		}
	}
	return recorded, nil
}

func queryInt(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return fallback
	}
	return value
}

func intValue(raw any, fallback int) int {
	value, ok := raw.(float64)
	if !ok {
		return fallback
	}
	return int(value)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
