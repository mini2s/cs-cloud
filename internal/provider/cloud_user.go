package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"cs-cloud/internal/cloud"
)

type CloudUser struct {
	ID         string `json:"id"`
	SubjectID  string `json:"subjectId"`
	Name       string `json:"name"`
	Username   string `json:"username"`
	Email      string `json:"email,omitempty"`
	Phone      string `json:"phone,omitempty"`
	AvatarURL  string `json:"avatarUrl"`
	Auth       *CloudUserAuth `json:"auth,omitempty"`
}

type CloudUserAuth struct {
	Provider        string `json:"provider"`
	ExternalKey     string `json:"externalKey"`
	ProviderUserID  string `json:"providerUserId"`
	ExternalSubject string `json:"externalSubject"`
}

func GetCloudCurrentUser(ctx context.Context, accessToken, credBaseURL string) (*CloudUser, error) {
	cc := cloud.NewClient(nil)
	url := cc.URL(cloud.PathAuthMe, credBaseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	cc.SetUserAuthHeaders(req, accessToken)

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("get current user failed: %d", resp.StatusCode)
	}

	var envelope struct {
		User *CloudUser `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode user response: %w", err)
	}
	if envelope.User == nil {
		return nil, fmt.Errorf("empty user response")
	}
	return envelope.User, nil
}
