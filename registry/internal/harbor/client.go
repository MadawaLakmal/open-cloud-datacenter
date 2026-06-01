// Package harbor wraps Harbor's native admin API for the per-tenant
// operations the registry operator needs: project lookup, robot CRUD,
// retention policy bootstrap, tag-immutability bootstrap, health probing.
//
// Each Client is bound to one tenant Harbor instance. The admin credential
// comes from the harbor-admin-credentials Secret in the Backend's namespace.
package harbor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is bound to one tenant Harbor instance.
type Client struct {
	BaseURL  string // e.g. http://harbor-harbor-core.dc-tenant-acme.svc.cluster.local
	Username string
	Password string
	HTTP     *http.Client
}

// New builds a Client with a 10s HTTP timeout.
func New(baseURL, user, pass string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Username: user,
		Password: pass,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Robot represents a Harbor robot account. Secret is populated only by CreateRobot.
type Robot struct {
	Name   string // "robot$<name>"
	ID     int    // Harbor's numeric id
	Secret string
}

// Retention is the simplified retention policy the operator applies at
// provision time. Either field at zero means "no cap on that dimension."
type Retention struct {
	KeepLastTags    int
	UntaggedTTLDays int
}

// Health calls Harbor's /api/v2.0/health. Returns nil when all components report healthy.
func (c *Client) Health(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2.0/health", nil)
	if err != nil {
		return fmt.Errorf("harbor health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("harbor health: unexpected status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("harbor health decode: %w", err)
	}
	if body.Status != "healthy" {
		return fmt.Errorf("harbor health: status %q", body.Status)
	}
	return nil
}

// CreateRobot creates a project-scoped robot account with the given scope.
// scope is "Pull" | "PushPull" | "Admin". The one-time secret is returned in
// Robot.Secret — Harbor never reveals it again.
func (c *Client) CreateRobot(ctx context.Context, project, name, scope string, expiresAt time.Time) (Robot, error) {
	access := scopeToAccess(scope)

	durationDays := int(time.Until(expiresAt).Hours() / 24)
	if durationDays < 1 {
		durationDays = 1
	}

	payload := map[string]any{
		"name":        name,
		"description": "",
		"duration":    durationDays,
		"disable":     false,
		"level":       "project",
		"permissions": []map[string]any{
			{
				"kind":      "project",
				"namespace": project,
				"access":    access,
			},
		},
	}

	resp, err := c.do(ctx, http.MethodPost,
		"/api/v2.0/projects/"+project+"/robots", payload)
	if err != nil {
		return Robot{}, fmt.Errorf("create robot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return Robot{}, fmt.Errorf("create robot: status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Robot{}, fmt.Errorf("create robot decode: %w", err)
	}
	return Robot{Name: result.Name, ID: result.ID, Secret: result.Secret}, nil
}

// DeleteRobot removes a robot by Harbor's numeric id. Idempotent — 404 is success.
func (c *Client) DeleteRobot(ctx context.Context, project string, id int) error {
	resp, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v2.0/projects/%s/robots/%d", project, id), nil)
	if err != nil {
		return fmt.Errorf("delete robot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete robot: status %d: %s", resp.StatusCode, body)
}

// EnableImmutableTagRule applies a "match all" immutability rule to the project.
// One-shot, called on first reconcile when spec.engineConfig.tagImmutability is true.
func (c *Client) EnableImmutableTagRule(ctx context.Context, project string) error {
	payload := map[string]any{
		"selector": map[string]any{
			"kind":       "doublestar",
			"decoration": "repoMatches",
			"pattern":    "**",
		},
		"tag_selectors": []map[string]any{
			{
				"kind":       "doublestar",
				"decoration": "matches",
				"pattern":    "**",
			},
		},
	}
	resp, err := c.do(ctx, http.MethodPost,
		"/api/v2.0/projects/"+project+"/immutabletagrules", payload)
	if err != nil {
		return fmt.Errorf("enable immutable tags: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("enable immutable tags: status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// SetRetentionPolicy applies a retention rule to the project's repositories.
// One-shot, called on first reconcile when spec.engineConfig.retention is set.
func (c *Client) SetRetentionPolicy(ctx context.Context, project string, r Retention) error {
	rules := []map[string]any{}
	if r.KeepLastTags > 0 {
		rules = append(rules, map[string]any{
			"template": "latestPushedK",
			"params":   map[string]any{"latestPushedK": r.KeepLastTags},
			"tag_selectors": []map[string]any{
				{"kind": "doublestar", "decoration": "matches", "pattern": "**"},
			},
			"scope_selectors": map[string]any{
				"repository": []map[string]any{
					{"kind": "doublestar", "decoration": "repoMatches", "pattern": "**"},
				},
			},
		})
	}
	if r.UntaggedTTLDays > 0 {
		rules = append(rules, map[string]any{
			"template": "nDaysSinceLastPull",
			"params":   map[string]any{"nDaysSinceLastPull": r.UntaggedTTLDays},
			"tag_selectors": []map[string]any{
				{"kind": "doublestar", "decoration": "matches", "pattern": "**"},
			},
			"scope_selectors": map[string]any{
				"repository": []map[string]any{
					{"kind": "doublestar", "decoration": "repoMatches", "pattern": "**"},
				},
			},
		})
	}
	if len(rules) == 0 {
		return nil
	}
	payload := map[string]any{
		"algorithm": "or",
		"rules":     rules,
		"trigger":   map[string]any{"kind": "Schedule", "settings": map[string]any{"cron": "0 0 * * *"}},
		"scope":     map[string]any{"level": "project", "ref": 0},
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/v2.0/retentions", payload)
	if err != nil {
		return fmt.Errorf("set retention: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set retention: status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// ---------- internal helpers ----------

// do executes an authenticated HTTP request against the Harbor API.
// body may be nil for requests with no request body.
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.HTTP.Do(req)
}

// scopeToAccess converts our token scope string to the Harbor access entries
// the robot-create API expects.
func scopeToAccess(scope string) []map[string]any {
	type entry = map[string]any
	resource := func(r, a string) entry { return entry{"resource": r, "action": a} }

	switch scope {
	case "Pull":
		return []entry{
			resource("repository", "pull"),
			resource("artifact", "read"),
		}
	case "PushPull":
		return []entry{
			resource("repository", "pull"),
			resource("repository", "push"),
			resource("artifact", "read"),
			resource("artifact", "delete"),
		}
	default: // "Admin"
		return []entry{
			resource("repository", "pull"),
			resource("repository", "push"),
			resource("repository", "delete"),
			resource("artifact", "read"),
			resource("artifact", "delete"),
			resource("artifact", "create"),
			resource("tag", "create"),
			resource("tag", "delete"),
			resource("tag", "list"),
			resource("accessory", "list"),
		}
	}
}
