package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
)

// ── finalizer helpers ─────────────────────────────────────────────────────────

func TestHasFinalizer(t *testing.T) {
	cases := []struct {
		name  string
		fs    []string
		want  bool
	}{
		{"present", []string{"a", BackendFinalizer, "c"}, true},
		{"absent", []string{"a", "c"}, false},
		{"empty slice", []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasFinalizer(tc.fs, BackendFinalizer); got != tc.want {
				t.Fatalf("hasFinalizer() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAddFinalizer(t *testing.T) {
	t.Run("adds when absent", func(t *testing.T) {
		got := addFinalizer([]string{"a"}, BackendFinalizer)
		if !hasFinalizer(got, BackendFinalizer) {
			t.Fatal("finalizer not added")
		}
	})
	t.Run("no-op when already present", func(t *testing.T) {
		in := []string{BackendFinalizer}
		got := addFinalizer(in, BackendFinalizer)
		count := 0
		for _, f := range got {
			if f == BackendFinalizer {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected exactly one copy, got %d", count)
		}
	})
}

func TestRemoveFinalizer(t *testing.T) {
	t.Run("removes present finalizer", func(t *testing.T) {
		got := removeFinalizer([]string{"a", BackendFinalizer, "c"}, BackendFinalizer)
		if hasFinalizer(got, BackendFinalizer) {
			t.Fatal("finalizer still present after remove")
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 remaining, got %d", len(got))
		}
	})
	t.Run("no-op when absent", func(t *testing.T) {
		got := removeFinalizer([]string{"a", "c"}, BackendFinalizer)
		if len(got) != 2 {
			t.Fatalf("expected 2 unchanged, got %d", len(got))
		}
	})
}

// ── label propagation ─────────────────────────────────────────────────────────

func TestPropagateLabels(t *testing.T) {
	t.Run("copies all seven labels", func(t *testing.T) {
		parent := map[string]string{
			LabelTenant:       "acme",
			LabelProject:      "billing",
			LabelTenantUUID:   "t-uuid",
			LabelProjectUUID:  "p-uuid",
			LabelResourceUUID: "r-uuid",
			LabelResourceKind: "Registry",
			LabelResourceName: "harbor",
		}
		child := PropagateLabels(parent, nil)
		for k, v := range parent {
			if child[k] != v {
				t.Errorf("label %q: got %q, want %q", k, child[k], v)
			}
		}
	})
	t.Run("preserves existing child labels", func(t *testing.T) {
		parent := map[string]string{LabelTenant: "acme"}
		child := map[string]string{"existing": "keep"}
		result := PropagateLabels(parent, child)
		if result["existing"] != "keep" {
			t.Fatal("existing child label was overwritten")
		}
	})
	t.Run("skips empty parent values", func(t *testing.T) {
		parent := map[string]string{LabelTenant: ""}
		child := PropagateLabels(parent, nil)
		if _, ok := child[LabelTenant]; ok {
			t.Fatal("empty parent label should not be propagated")
		}
	})
	t.Run("nil parent produces empty child", func(t *testing.T) {
		child := PropagateLabels(nil, nil)
		if child == nil {
			t.Fatal("returned nil map")
		}
	})
}

// ── generatePassword ──────────────────────────────────────────────────────────

func TestGeneratePassword(t *testing.T) {
	t.Run("returns 24-char URL-safe base64", func(t *testing.T) {
		pw, err := generatePassword()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pw) != 24 {
			t.Fatalf("expected 24 chars, got %d (%q)", len(pw), pw)
		}
	})
	t.Run("produces unique values", func(t *testing.T) {
		seen := map[string]bool{}
		for i := 0; i < 50; i++ {
			pw, err := generatePassword()
			if err != nil {
				t.Fatalf("unexpected error at i=%d: %v", i, err)
			}
			if seen[pw] {
				t.Fatalf("duplicate password generated at i=%d", i)
			}
			seen[pw] = true
		}
	})
}

// ── setReadyCondition ─────────────────────────────────────────────────────────

func TestSetReadyCondition(t *testing.T) {
	t.Run("appends when no conditions exist", func(t *testing.T) {
		var conds []metav1.Condition
		setReadyCondition(&conds, metav1.ConditionTrue, "HarborReady", "ok", 1)
		if len(conds) != 1 {
			t.Fatalf("expected 1 condition, got %d", len(conds))
		}
		if conds[0].Type != "Ready" {
			t.Fatalf("expected type Ready, got %q", conds[0].Type)
		}
		if conds[0].Status != metav1.ConditionTrue {
			t.Fatalf("expected True, got %q", conds[0].Status)
		}
	})
	t.Run("updates existing condition without duplicating", func(t *testing.T) {
		var conds []metav1.Condition
		setReadyCondition(&conds, metav1.ConditionFalse, "NotReady", "pending", 1)
		setReadyCondition(&conds, metav1.ConditionTrue, "HarborReady", "ok", 2)
		if len(conds) != 1 {
			t.Fatalf("expected 1 condition after update, got %d", len(conds))
		}
		if conds[0].Status != metav1.ConditionTrue {
			t.Fatalf("expected True after update, got %q", conds[0].Status)
		}
		if conds[0].ObservedGeneration != 2 {
			t.Fatalf("expected observedGeneration=2, got %d", conds[0].ObservedGeneration)
		}
	})
}

// ── makeResourceRef ───────────────────────────────────────────────────────────

func TestMakeResourceRef(t *testing.T) {
	ref := makeResourceRef("", "v1", "Secret", "dc-tenant-acme", "harbor-admin-credentials")
	if ref.Version != "v1" || ref.Kind != "Secret" || ref.Namespace != "dc-tenant-acme" || ref.Name != "harbor-admin-credentials" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}

// ── Phase enum completeness ───────────────────────────────────────────────────

func TestPhaseValues(t *testing.T) {
	phases := []registryv1alpha1.Phase{
		registryv1alpha1.PhasePending,
		registryv1alpha1.PhaseProvisioning,
		registryv1alpha1.PhaseReady,
		registryv1alpha1.PhaseFailed,
		registryv1alpha1.PhaseTerminating,
	}
	seen := map[registryv1alpha1.Phase]bool{}
	for _, p := range phases {
		if seen[p] {
			t.Fatalf("duplicate phase value: %q", p)
		}
		seen[p] = true
		if p == "" {
			t.Fatal("phase must not be empty string")
		}
	}
}
