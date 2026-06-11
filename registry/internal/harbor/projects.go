package harbor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrProjectNotFound is returned by GetProjectByName when the named project
// does not exist in the bound Harbor. Use errors.Is to detect.
var ErrProjectNotFound = errors.New("harbor: project not found")

// Project is the subset of Harbor's project model the operator cares about.
type Project struct {
	ID     int    `json:"project_id"`
	Name   string `json:"name"`
	Public bool   // derived from metadata.public ("true"/"false" string)
}

// CreateProject ensures a Harbor-project with the given name and visibility
// exists. Returns the project's numeric ID.
//
// If the project already exists its visibility is reconciled to match public
// (idempotent drift correction). If it doesn't exist it is created.
func (c *Client) CreateProject(ctx context.Context, name string, public bool) (int, error) {
	// Fast path: project already exists — reconcile visibility drift.
	if existing, err := c.GetProjectByName(ctx, name); err == nil {
		if existing.Public != public {
			if uerr := c.UpdateProjectVisibility(ctx, existing.ID, public); uerr != nil {
				return 0, fmt.Errorf("harbor reconcile project visibility: %w", uerr)
			}
		}
		return existing.ID, nil
	} else if !errors.Is(err, ErrProjectNotFound) {
		return 0, err
	}

	payload := map[string]any{
		"project_name": name,
		"metadata": map[string]string{
			"public": boolString(public),
		},
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/v2.0/projects", payload)
	if err != nil {
		return 0, fmt.Errorf("harbor create project: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		// Harbor returns 201 with an empty body and a Location header like
		// "/api/v2.0/projects/42". Parse the trailing id.
		loc := resp.Header.Get("Location")
		id, err := parseProjectIDFromLocation(loc)
		if err != nil {
			// Fall back to a name lookup.
			p, lerr := c.GetProjectByName(ctx, name)
			if lerr != nil {
				return 0, fmt.Errorf("harbor create project: parse location %q: %w; lookup: %v", loc, err, lerr)
			}
			return p.ID, nil
		}
		return id, nil
	case http.StatusConflict:
		// Race with another reconciler — fall through to lookup.
		p, err := c.GetProjectByName(ctx, name)
		if err != nil {
			return 0, fmt.Errorf("harbor create project conflict, lookup: %w", err)
		}
		return p.ID, nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("harbor create project: status %d: %s", resp.StatusCode, body)
	}
}

// GetProjectByName looks up a project by name. Returns ErrProjectNotFound
// (wrappable via errors.Is) when missing.
func (c *Client) GetProjectByName(ctx context.Context, name string) (Project, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2.0/projects?name="+name, nil)
	if err != nil {
		return Project{}, fmt.Errorf("harbor get project: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Project{}, ErrProjectNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Project{}, fmt.Errorf("harbor get project: status %d: %s", resp.StatusCode, body)
	}

	var list []struct {
		ProjectID int    `json:"project_id"`
		Name      string `json:"name"`
		Metadata  struct {
			Public string `json:"public"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return Project{}, fmt.Errorf("harbor get project decode: %w", err)
	}
	for _, p := range list {
		if p.Name == name {
			return Project{ID: p.ProjectID, Name: p.Name, Public: p.Metadata.Public == "true"}, nil
		}
	}
	return Project{}, ErrProjectNotFound
}

// DeleteProject removes a project by numeric ID. Idempotent — 404 is success.
func (c *Client) DeleteProject(ctx context.Context, id int) error {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v2.0/projects/%d", id), nil)
	if err != nil {
		return fmt.Errorf("harbor delete project: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("harbor delete project: status %d: %s", resp.StatusCode, body)
}

// UpdateProjectVisibility flips the public/private flag on an existing project.
func (c *Client) UpdateProjectVisibility(ctx context.Context, id int, public bool) error {
	payload := map[string]any{
		"metadata": map[string]string{"public": boolString(public)},
	}
	resp, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v2.0/projects/%d", id), payload)
	if err != nil {
		return fmt.Errorf("harbor update project: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("harbor update project: status %d: %s", resp.StatusCode, body)
}

// ---------- helpers ----------

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// parseProjectIDFromLocation extracts the trailing integer from a Harbor
// Location header like "/api/v2.0/projects/42".
func parseProjectIDFromLocation(loc string) (int, error) {
	if loc == "" {
		return 0, errors.New("empty location header")
	}
	lastSlash := -1
	for i := len(loc) - 1; i >= 0; i-- {
		if loc[i] == '/' {
			lastSlash = i
			break
		}
	}
	if lastSlash < 0 || lastSlash == len(loc)-1 {
		return 0, fmt.Errorf("bad location %q", loc)
	}
	var id int
	if _, err := fmt.Sscanf(loc[lastSlash+1:], "%d", &id); err != nil {
		return 0, fmt.Errorf("bad location id in %q: %w", loc, err)
	}
	return id, nil
}
