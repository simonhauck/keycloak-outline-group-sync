//go:build e2e

package e2e

import (
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var outlinePublicURL = func() *url.URL {
	parsed, err := url.Parse(outlineURL)
	if err != nil {
		panic(err)
	}
	return parsed
}()

var loginFormAction = regexp.MustCompile(`(?s)<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)

// login runs the OIDC authorization-code flow with plain HTTP, as a browser
// would: it starts at Outline, follows the redirect to Keycloak's login form,
// posts credentials, and follows the callback back to Outline.
//
// The test runs on the host, so connections to Keycloak's internal service
// name are dialed at its published loopback port while the Host header stays
// keycloak:8080. Keycloak derives the token issuer from the request host, and
// a token issued for one host is rejected at the userinfo endpoint of another;
// keeping the internal name throughout is what makes Outline's server-side
// token exchange and userinfo calls line up.
func (s *stack) login(t *testing.T, username, password string) string {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("creating cookie jar: %v", err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == "keycloak:8080" {
					address = "localhost:18080"
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			},
		},
	}

	page, pageURL := getPage(t, client, outlineURL+"/auth/oidc")
	action := loginFormAction.FindStringSubmatch(string(page))
	if action == nil {
		t.Fatalf("Keycloak login form not found at %s; page:\n%s", pageURL, tail(string(page), 2000))
	}
	actionURL, err := pageURL.Parse(html.UnescapeString(action[1]))
	if err != nil {
		t.Fatalf("parsing Keycloak login form action %q: %v", action[1], err)
	}

	form := url.Values{
		"username":     {username},
		"password":     {password},
		"credentialId": {""},
		"login":        {"Sign In"},
	}
	request, err := http.NewRequest(http.MethodPost, actionURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("building login request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("posting Keycloak login for %q: %v", username, err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("reading login response: %v", err)
	}

	for _, cookie := range jar.Cookies(outlinePublicURL) {
		if cookie.Name == "accessToken" {
			return cookie.Value
		}
	}
	t.Fatalf("SSO login for %q did not yield an Outline accessToken cookie; ended at %s; page:\n%s",
		username, response.Request.URL, tail(string(body), 2000))
	return ""
}

func getPage(t *testing.T, client *http.Client, rawURL string) ([]byte, *url.URL) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading GET %s: %v", rawURL, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d:\n%s", rawURL, response.StatusCode, tail(string(body), 2000))
	}
	return body, response.Request.URL
}
