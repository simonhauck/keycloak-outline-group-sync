package app_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type fakeKeycloak struct {
	realm    string
	clients  []keycloakClientRep
	roles    []clientRole
	pageSize int

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

type fakeOutline struct {
	pageSize        int
	listGroupsDelay time.Duration
	failListGroups  int

	mu          sync.Mutex
	groups      []outlineGroup
	counter     int
	requests    []recordedRequest
	inFlight    int
	maxInFlight int
	server      *httptest.Server
}

func newFakeOutline(t *testing.T) *fakeOutline {
	t.Helper()
	f := &fakeOutline{}
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

func (f *fakeOutline) writeRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var writes []recordedRequest
	for _, request := range f.requests {
		if request.Path != "/api/groups.list" {
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
