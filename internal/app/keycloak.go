package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const keycloakPageSize = 100

type keycloakAdmin struct {
	baseURL string
	realm   string
	http    *http.Client
	token   string
}

type clientRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type keycloakUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Enabled  bool   `json:"enabled"`
}

func newKeycloakAdmin(baseURL, realm string) *keycloakAdmin {
	return &keycloakAdmin{
		baseURL: strings.TrimRight(baseURL, "/"),
		realm:   realm,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *keycloakAdmin) authenticate(ctx context.Context, clientID, clientSecret string) error {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	endpoint := c.baseURL + "/realms/" + url.PathEscape(c.realm) + "/protocol/openid-connect/token"
	body, status, err := c.do(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("token request returned status %d: %s", status, strings.TrimSpace(string(body)))
	}

	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}
	if token.AccessToken == "" {
		return fmt.Errorf("token response contained no access token")
	}
	c.token = token.AccessToken
	return nil
}

func (c *keycloakAdmin) clientUUID(ctx context.Context, clientID string) (string, error) {
	endpoint := c.baseURL + "/admin/realms/" + url.PathEscape(c.realm) + "/clients?clientId=" + url.QueryEscape(clientID)
	body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("client lookup returned status %d: %s", status, strings.TrimSpace(string(body)))
	}

	var clients []struct {
		ID       string `json:"id"`
		ClientID string `json:"clientId"`
	}
	if err := json.Unmarshal(body, &clients); err != nil {
		return "", fmt.Errorf("decoding client lookup response: %w", err)
	}
	for _, client := range clients {
		if client.ClientID == clientID {
			return client.ID, nil
		}
	}
	return "", fmt.Errorf("client %q not found in realm %q", clientID, c.realm)
}

func (c *keycloakAdmin) clientRoles(ctx context.Context, clientUUID string) ([]clientRole, error) {
	return listAllPages(func(first int) ([]clientRole, error) {
		endpoint := fmt.Sprintf("%s/admin/realms/%s/clients/%s/roles?first=%d&max=%d",
			c.baseURL, url.PathEscape(c.realm), url.PathEscape(clientUUID), first, keycloakPageSize)
		body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("role listing returned status %d: %s", status, strings.TrimSpace(string(body)))
		}

		var page []clientRole
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding role listing response: %w", err)
		}
		return page, nil
	})
}

func (c *keycloakAdmin) users(ctx context.Context) ([]keycloakUser, error) {
	return listAllPages(func(first int) ([]keycloakUser, error) {
		endpoint := fmt.Sprintf("%s/admin/realms/%s/users?first=%d&max=%d",
			c.baseURL, url.PathEscape(c.realm), first, keycloakPageSize)
		body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("user listing returned status %d: %s", status, strings.TrimSpace(string(body)))
		}

		var page []keycloakUser
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding user listing response: %w", err)
		}
		return page, nil
	})
}

func listAllPages[T any](fetch func(first int) ([]T, error)) ([]T, error) {
	var all []T
	for first := 0; ; {
		page, err := fetch(first)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 {
			return all, nil
		}
		first += len(page)
	}
}

func (c *keycloakAdmin) effectiveClientRoles(ctx context.Context, userID, clientUUID string) ([]clientRole, error) {
	endpoint := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/clients/%s/composite",
		c.baseURL, url.PathEscape(c.realm), url.PathEscape(userID), url.PathEscape(clientUUID))
	body, status, err := c.do(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("role mapping lookup returned status %d: %s", status, strings.TrimSpace(string(body)))
	}

	var roles []clientRole
	if err := json.Unmarshal(body, &roles); err != nil {
		return nil, fmt.Errorf("decoding role mapping response: %w", err)
	}
	return roles, nil
}

func (c *keycloakAdmin) do(ctx context.Context, method, endpoint string, body io.Reader, contentType string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, err
	}
	return responseBody, response.StatusCode, nil
}
