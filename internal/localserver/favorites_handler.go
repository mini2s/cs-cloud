package localserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
)

const favoritePageSize = 100
const favoriteMaxPages = 20

var storeTypeMap = map[string]string{
	"skill":    "skill",
	"subagent": "agent",
	"command":  "command",
	"mcp":      "mcp",
}

// favoriteItem represents a single cloud favorite item with its current status.
type favoriteItem struct {
	ID          string `json:"id" example:"abc123"`
	Slug        string `json:"slug" example:"my-skill"`
	Name        string `json:"name" example:"My Skill"`
	Description string `json:"description" example:"A useful skill"`
	ItemType    string `json:"itemType" example:"skill"`
	Status      string `json:"status" example:"Active"`
	LocalPath   string `json:"localPath,omitempty" example:"/home/user/.config/costrict/skills/my-skill"`
}

// favoriteActionResponse represents the response from a favorite load/unload action.
type favoriteActionResponse struct {
	Success bool   `json:"success" example:"true"`
	Slug    string `json:"slug" example:"my-skill"`
}

type remoteListResponse struct {
	Items   []json.RawMessage `json:"items"`
	HasMore bool              `json:"hasMore"`
}

func ensureCloudCredentials(ctx context.Context) (*provider.Credentials, error) {
	creds, err := provider.LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if creds == nil || creds.AccessToken == "" {
		return nil, fmt.Errorf("not authenticated")
	}

	if creds.RefreshToken != "" && !provider.IsTokenValid(creds.AccessToken, creds.RefreshToken, creds.ExpiryDate) {
		cc := cloud.NewClient(nil)
		oidcBase := cc.OIDCBaseURL(creds.BaseURL)
		refreshed, err := provider.RefreshCoStrictToken(
			oidcBase,
			creds.RefreshToken,
			creds.State,
		)
		if err != nil {
			return nil, fmt.Errorf("token expired and refresh failed: %w", err)
		}
		expiry := provider.ExtractExpiryFromJWT(refreshed.AccessToken)
		fresh := &provider.Credentials{
			ID:           creds.ID,
			Name:         creds.Name,
			AccessToken:  refreshed.AccessToken,
			RefreshToken: refreshed.RefreshToken,
			State:        creds.State,
			MachineID:    creds.MachineID,
			BaseURL:      oidcBase,
			ExpiryDate:   expiry,
			UpdatedAt:    time.Now().Format(time.RFC3339),
			ExpiredAt:    time.UnixMilli(expiry).Format(time.RFC3339),
		}
		if err := provider.SaveCredentials(fresh); err != nil {
			return nil, fmt.Errorf("save refreshed credentials: %w", err)
		}
		creds = fresh
	}

	return creds, nil
}

func fetchCloudFavorites(ctx context.Context, itemType string) ([]favoriteItem, error) {
	creds, err := ensureCloudCredentials(ctx)
	if err != nil {
		return nil, err
	}

	cc := cloud.NewClient(nil)
	baseURL := cc.CloudBaseURL(creds.BaseURL)

	var result []favoriteItem
	for page := 1; page <= favoriteMaxPages; page++ {
		params := url.Values{}
		params.Set("page", strconv.Itoa(page))
		params.Set("pageSize", strconv.Itoa(favoritePageSize))
		params.Set("favorited", "true")
		if itemType != "" {
			params.Set("type", itemType)
		}

		apiURL := fmt.Sprintf("%s/api/items?%s", baseURL, params.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := cc.HTTPClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("authentication failed: %d", resp.StatusCode)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("api error: %d %s", resp.StatusCode, string(body))
		}

		var data remoteListResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		for _, raw := range data.Items {
			var item map[string]any
			if err := json.Unmarshal(raw, &item); err != nil {
				continue
			}
			parsed := parseFavoriteListItem(item)
			if parsed != nil {
				result = append(result, *parsed)
			}
		}

		if !data.HasMore || len(data.Items) == 0 {
			break
		}
	}

	return result, nil
}

func parseFavoriteListItem(data map[string]any) *favoriteItem {
	storeType, _ := data["itemType"].(string)
	localType := storeTypeMap[storeType]
	if localType == "" {
		return nil
	}

	id, _ := data["id"].(string)
	if id == "" {
		return nil
	}

	slug, _ := data["slug"].(string)
	if slug == "" {
		slug = id
	}

	name, _ := data["name"].(string)
	if name == "" {
		name = slug
	}

	description, _ := data["description"].(string)

	return &favoriteItem{
		ID:          id,
		Slug:        slug,
		Name:        name,
		Description: description,
		ItemType:    localType,
		Status:      "Cloud",
	}
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "not authenticated") ||
		strings.Contains(msg, "token expired")
}

func (s *Server) fetchLocalFavorites(r *http.Request) ([]favoriteItem, error) {
	recorder := httptest.NewRecorder()
	s.handleProxy(recorder, r)

	body, err := io.ReadAll(recorder.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Bun server may return gzip-compressed responses; decompress before parsing.
	if recorder.Header().Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gr.Close()
		body, err = io.ReadAll(gr)
		if err != nil {
			return nil, fmt.Errorf("gzip decompress: %w", err)
		}
	}

	if recorder.Code != http.StatusOK {
		return nil, fmt.Errorf("proxy returned %d", recorder.Code)
	}

	// Try array format first (newer cs versions)
	var items []favoriteItem
	if err := json.Unmarshal(body, &items); err == nil {
		return items, nil
	}

	// Fall back to wrapped object format (cs <= 3.0.33)
	var wrapped struct {
		Success bool           `json:"success"`
		Items   []favoriteItem `json:"items"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Success {
		return wrapped.Items, nil
	}

	return nil, fmt.Errorf("cannot unmarshal response: %s", string(body))
}

func mergeFavorites(cloud, local []favoriteItem) []favoriteItem {
	localMap := make(map[string]favoriteItem, len(local))
	for _, item := range local {
		localMap[item.Slug] = item
	}

	merged := make([]favoriteItem, len(cloud))
	for i, item := range cloud {
		if localItem, ok := localMap[item.Slug]; ok {
			item.Status = localItem.Status
			item.LocalPath = localItem.LocalPath
		}
		merged[i] = item
	}

	return merged
}

// @Summary      List favorite items
// @Description  List all cloud favorite items with their current status. Supports filtering by type via query parameter.
// @Tags         Agent
// @Produce      json
// @Param        type   query   string  false  "Filter by item type: skill, agent, command, mcp"
// @Success      200    {array}  favoriteItem
// @Failure      401    {object} envelope
// @Failure      500    {object} envelope
// @Router       /agents/favorites [get]
func (s *Server) handleFavoriteList(w http.ResponseWriter, r *http.Request) {
	itemType := r.URL.Query().Get("type")

	cloudItems, err := fetchCloudFavorites(r.Context(), itemType)
	if err != nil {
		logger.Error("fetch cloud favorites failed: %v", err)
		if isAuthError(err) {
			writeErr(w, http.StatusUnauthorized, "AUTH_REQUIRED", err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	localItems, localErr := s.fetchLocalFavorites(r)
	if localErr != nil {
		logger.Warn("fetch local favorites failed: %v", localErr)
	}

	merged := mergeFavorites(cloudItems, localItems)
	writeJSON(w, http.StatusOK, merged)
}

// @Summary      Load a favorite item
// @Description  Load a favorite skill, agent, command, or MCP into the current workspace.
// @Tags         Agent
// @Produce      json
// @Param        id     path    string  true  "Favorite item slug or ID"
// @Success      200    {object}  favoriteActionResponse
// @Failure      400    {object} envelope
// @Failure      500    {object} envelope
// @Router       /agents/favorites/{id}/load [post]
func (s *Server) handleFavoriteLoad(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Unload a favorite item
// @Description  Unload a favorite skill, agent, command, or MCP from the current workspace.
// @Tags         Agent
// @Produce      json
// @Param        id     path    string  true  "Favorite item slug or ID"
// @Success      200    {object}  favoriteActionResponse
// @Failure      400    {object} envelope
// @Failure      500    {object} envelope
// @Router       /agents/favorites/{id}/unload [post]
func (s *Server) handleFavoriteUnload(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}
