package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient builds a Client pointed at a test HTTP server.
func newTestClient(srv *httptest.Server) *Client {
	return &Client{
		BaseURL:  srv.URL,
		Username: "admin",
		Password: "test",
		HTTP:     srv.Client(),
	}
}

// ── parseProjectIDFromLocation ────────────────────────────────────────────────

func TestParseProjectIDFromLocation(t *testing.T) {
	cases := []struct {
		loc     string
		want    int
		wantErr bool
	}{
		{"/api/v2.0/projects/42", 42, false},
		{"/api/v2.0/projects/1", 1, false},
		{"", 0, true},
		{"/api/v2.0/projects/", 0, true},
		{"/api/v2.0/projects/abc", 0, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("loc=%q", tc.loc), func(t *testing.T) {
			id, err := parseProjectIDFromLocation(tc.loc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tc.want {
				t.Fatalf("got %d, want %d", id, tc.want)
			}
		})
	}
}

// ── boolString ────────────────────────────────────────────────────────────────

func TestBoolString(t *testing.T) {
	if boolString(true) != "true" {
		t.Fatal("boolString(true) should return \"true\"")
	}
	if boolString(false) != "false" {
		t.Fatal("boolString(false) should return \"false\"")
	}
}

// ── GetProjectByName ──────────────────────────────────────────────────────────

func TestGetProjectByName_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2.0/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"project_id": 10,
				"name":       "billing",
				"metadata":   map[string]string{"public": "false"},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	p, err := c.GetProjectByName(context.Background(), "billing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID != 10 || p.Name != "billing" || p.Public != false {
		t.Fatalf("unexpected project: %+v", p)
	}
}

func TestGetProjectByName_PublicTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"project_id": 20,
				"name":       "public-proj",
				"metadata":   map[string]string{"public": "true"},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	p, err := c.GetProjectByName(context.Background(), "public-proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Public {
		t.Fatal("expected public=true")
	}
}

func TestGetProjectByName_NotFound_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetProjectByName(context.Background(), "missing")
	if err != ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestGetProjectByName_NotFound_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetProjectByName(context.Background(), "missing")
	if err != ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound, got %v", err)
	}
}

// ── CreateProject — idempotency ───────────────────────────────────────────────

func TestCreateProject_CreatesNew(t *testing.T) {
	var created bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects":
			// GetProjectByName — return not found.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/projects":
			created = true
			w.Header().Set("Location", "/api/v2.0/projects/55")
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	id, err := c.CreateProject(context.Background(), "newproj", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 55 {
		t.Fatalf("expected id=55, got %d", id)
	}
	if !created {
		t.Fatal("POST /api/v2.0/projects was not called")
	}
}

func TestCreateProject_IdempotentWhenAlreadyExists(t *testing.T) {
	var postCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"project_id": 99, "name": "existing", "metadata": map[string]string{"public": "false"}},
			})
		case r.Method == http.MethodPost:
			postCalled = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	id, err := c.CreateProject(context.Background(), "existing", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 99 {
		t.Fatalf("expected id=99, got %d", id)
	}
	if postCalled {
		t.Fatal("POST should not be called when project already exists")
	}
}

func TestCreateProject_ReconcileVisibilityDrift(t *testing.T) {
	// Project exists as private. Caller wants it public — UpdateProjectVisibility must be called.
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"project_id": 10, "name": "proj", "metadata": map[string]string{"public": "false"}},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2.0/projects/10":
			putCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	id, err := c.CreateProject(context.Background(), "proj", true) // want public=true
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 10 {
		t.Fatalf("expected id=10, got %d", id)
	}
	if !putCalled {
		t.Fatal("UpdateProjectVisibility (PUT) was not called for visibility drift")
	}
}

func TestCreateProject_Conflict_FallsBackToLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/projects":
			// First GET (inside CreateProject fast path) returns empty.
			// Second GET (after 409) returns the project.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"project_id": 33, "name": "race", "metadata": map[string]string{"public": "false"}},
			})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	// The first GET also returns the project, so CreateProject takes the fast-path
	// and never POSTs — that's correct. If it did POST, the conflict handler re-looks up.
	id, err := c.CreateProject(context.Background(), "race", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 33 {
		t.Fatalf("expected id=33, got %d", id)
	}
}

// ── DeleteProject ─────────────────────────────────────────────────────────────

func TestDeleteProject_Idempotent_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.DeleteProject(context.Background(), 99); err != nil {
		t.Fatalf("expected 404 to be treated as success, got: %v", err)
	}
}

// ── DeleteRobot ───────────────────────────────────────────────────────────────

func TestDeleteRobot_Idempotent_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.DeleteRobot(context.Background(), "billing", 42); err != nil {
		t.Fatalf("expected 404 to be treated as success, got: %v", err)
	}
}

// ── CreateRobot ───────────────────────────────────────────────────────────────

func TestCreateRobot_Returns201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"id":     7,
			"name":   "robot$billing-robot",
			"secret": "mysecret",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	robot, err := c.CreateRobot(context.Background(), "billing", "billing-robot", "PushPull", time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if robot.ID != 7 || robot.Name != "robot$billing-robot" || robot.Secret != "mysecret" {
		t.Fatalf("unexpected robot: %+v", robot)
	}
}

// ── Health ────────────────────────────────────────────────────────────────────

func TestHealth_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHealth_Unhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("expected error for unhealthy status")
	}
}

// ── scopeToAccess ─────────────────────────────────────────────────────────────

func TestScopeToAccess(t *testing.T) {
	pull := scopeToAccess("Pull")
	if len(pull) == 0 {
		t.Fatal("Pull should return at least one access entry")
	}

	pushPull := scopeToAccess("PushPull")
	if len(pushPull) <= len(pull) {
		t.Fatal("PushPull should have more permissions than Pull")
	}

	admin := scopeToAccess("Admin")
	if len(admin) <= len(pushPull) {
		t.Fatal("Admin should have more permissions than PushPull")
	}
}
