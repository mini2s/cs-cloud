package cloud

import (
	"net/http"
	"strings"

	"cs-cloud/internal/config"
	"cs-cloud/internal/platform"
)

const cloudAPIPrefix = "cloud-api"

type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Client{
		cfg:        cfg,
		httpClient: platform.HTTPClient(),
	}
}

func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

func (c *Client) CloudBaseURL(credBaseURL string) string {
	raw := trimRight(firstNonEmpty(
		c.cfg.CloudBaseURL,
		credBaseURL,
		c.cfg.BaseURL,
		platform.Getenv("COSTRICT_CLOUD_BASE_URL"),
		platform.Getenv("COSTRICT_BASE_URL"),
		platform.DefaultCloudBaseURL,
	), "/")
	if strings.HasSuffix(raw, "/"+cloudAPIPrefix) {
		return raw
	}
	if c.cfg.CloudBaseURL != "" || platform.Getenv("COSTRICT_CLOUD_BASE_URL") != "" {
		return raw
	}
	return raw + "/" + cloudAPIPrefix
}

func (c *Client) OIDCBaseURL(credBaseURL string) string {
	raw := firstNonEmpty(
		platform.Getenv("COSTRICT_BASE_URL"),
		c.cfg.CloudBaseURL,
		credBaseURL,
		c.cfg.BaseURL,
		platform.DefaultCloudBaseURL,
	)
	raw = strings.TrimSuffix(raw, "/chat-rag/api/v1")
	raw = strings.TrimRight(raw, "/")
	return raw
}

func (c *Client) URL(path, credBaseURL string) string {
	base := c.CloudBaseURL(credBaseURL)
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

func (c *Client) SetDeviceAuthHeaders(req *http.Request, deviceToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
}

func (c *Client) SetDeviceAuthHeadersWithUser(req *http.Request, deviceToken, userAccessToken string) {
	c.SetDeviceAuthHeaders(req, deviceToken)
	if userAccessToken != "" {
		req.Header.Set("X-User-Token", userAccessToken)
	}
}

func (c *Client) SetUserAuthHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
}

func trimRight(s, suffix string) string {
	for len(s) > 0 && strings.HasSuffix(s, suffix) {
		s = s[:len(s)-len(suffix)]
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
