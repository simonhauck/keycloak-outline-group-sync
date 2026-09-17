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
		envelope, err := o.call(ctx, "groups.list", map[string]any{"limit": outlinePageSize, "offset": offset}, &data)
		if err != nil {
			return nil, err
		}
		groups = append(groups, data.Groups...)
		offset += len(data.Groups)
		if len(data.Groups) == 0 || offset >= envelope.Pagination.Total {
			return groups, nil
		}
	}
}

func (o *outlineClient) createGroup(ctx context.Context, name, externalID string) (outlineGroup, error) {
	var data struct {
		Group outlineGroup `json:"group"`
	}
	_, err := o.call(ctx, "groups.create", map[string]any{"name": name, "externalId": externalID}, &data)
	return data.Group, err
}

func (o *outlineClient) updateGroup(ctx context.Context, id, name, externalID string) (outlineGroup, error) {
	var data struct {
		Group outlineGroup `json:"group"`
	}
	_, err := o.call(ctx, "groups.update", map[string]any{"id": id, "name": name, "externalId": externalID}, &data)
	return data.Group, err
}

type outlineEnvelope struct {
	OK         bool            `json:"ok"`
	Error      string          `json:"error"`
	Data       json.RawMessage `json:"data"`
	Pagination struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
		Total  int `json:"total"`
	} `json:"pagination"`
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
