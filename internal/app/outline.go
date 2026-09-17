package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const outlinePageSize = 100

type outlineGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExternalID string `json:"externalId"`
}

type outlineUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type outlineClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newOutlineClient(baseURL, token string) *outlineClient {
	return &outlineClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *outlineClient) listGroups(ctx context.Context) ([]outlineGroup, error) {
	var groups []outlineGroup
	for offset := 0; ; {
		var data struct {
			Groups []outlineGroup `json:"groups"`
		}
		_, err := o.call(ctx, "groups.list", map[string]any{"limit": outlinePageSize, "offset": offset}, &data)
		if err != nil {
			return nil, err
		}
		groups = append(groups, data.Groups...)
		if len(data.Groups) == 0 {
			return groups, nil
		}
		offset += len(data.Groups)
	}
}

func (o *outlineClient) createGroup(ctx context.Context, name, externalID string) (outlineGroup, error) {
	var group outlineGroup
	_, err := o.call(ctx, "groups.create", map[string]any{"name": name, "externalId": externalID}, &group)
	return group, err
}

func (o *outlineClient) updateGroup(ctx context.Context, id, name, externalID string) (outlineGroup, error) {
	var group outlineGroup
	_, err := o.call(ctx, "groups.update", map[string]any{"id": id, "name": name, "externalId": externalID}, &group)
	return group, err
}

func (o *outlineClient) listUsersByEmails(ctx context.Context, emails []string) ([]outlineUser, error) {
	var users []outlineUser
	for start := 0; start < len(emails); start += outlinePageSize {
		batch := emails[start:min(start+outlinePageSize, len(emails))]
		for offset := 0; ; {
			var page []outlineUser
			_, err := o.call(ctx, "users.list", map[string]any{"emails": batch, "limit": outlinePageSize, "offset": offset}, &page)
			if err != nil {
				return nil, err
			}
			users = append(users, page...)
			if len(page) == 0 {
				break
			}
			offset += len(page)
		}
	}
	return users, nil
}

func (o *outlineClient) groupMembers(ctx context.Context, groupID string) ([]outlineUser, error) {
	var members []outlineUser
	for offset := 0; ; {
		var data struct {
			GroupMemberships []struct {
				UserID string `json:"userId"`
			} `json:"groupMemberships"`
			Users []outlineUser `json:"users"`
		}
		_, err := o.call(ctx, "groups.memberships", map[string]any{"id": groupID, "limit": outlinePageSize, "offset": offset}, &data)
		if err != nil {
			return nil, err
		}
		if len(data.Users) > 0 {
			members = append(members, data.Users...)
		} else {
			for _, membership := range data.GroupMemberships {
				members = append(members, outlineUser{ID: membership.UserID})
			}
		}
		if len(data.GroupMemberships) == 0 {
			return members, nil
		}
		offset += len(data.GroupMemberships)
	}
}

func (o *outlineClient) addUserToGroup(ctx context.Context, groupID, userID string) error {
	_, err := o.call(ctx, "groups.add_user", map[string]any{"id": groupID, "userId": userID}, nil)
	return err
}

func (o *outlineClient) removeUserFromGroup(ctx context.Context, groupID, userID string) error {
	_, err := o.call(ctx, "groups.remove_user", map[string]any{"id": groupID, "userId": userID}, nil)
	return err
}

type outlineEnvelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

func (o *outlineClient) call(ctx context.Context, route string, payload, data any) (outlineEnvelope, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return outlineEnvelope{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/"+route, bytes.NewReader(body))
	if err != nil {
		return outlineEnvelope{}, err
	}
	request.Header.Set("Authorization", "Bearer "+o.token)
	request.Header.Set("Content-Type", "application/json")

	response, err := o.http.Do(request)
	if err != nil {
		return outlineEnvelope{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return outlineEnvelope{}, err
	}

	var envelope outlineEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return outlineEnvelope{}, fmt.Errorf("outline %s returned unreadable response (status %d): %w", route, response.StatusCode, err)
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		message := strings.TrimSpace(envelope.Error)
		if message == "" {
			message = strings.TrimSpace(string(responseBody))
		}
		return envelope, fmt.Errorf("outline %s failed with status %d: %s", route, response.StatusCode, message)
	}
	if data != nil {
		if err := json.Unmarshal(envelope.Data, data); err != nil {
			return envelope, fmt.Errorf("outline %s returned unexpected data: %w", route, err)
		}
	}
	return envelope, nil
}
