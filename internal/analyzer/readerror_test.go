package analyzer

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyReadError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`Error from server (Forbidden): pods is forbidden`), true},
		{errors.New(`error: You must be logged in (Unauthorized)`), true},
		{errors.New(`Error from server (NotFound): deployments.apps "x" not found`), false},
		{errors.New(`context deadline exceeded`), false},
	}
	for _, c := range cases {
		if got := classifyReadError(c.err); got != c.want {
			t.Fatalf("classifyReadError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestRBACAwareSkip(t *testing.T) {
	forbidden := rbacAwareSkip("pvc", errors.New(`pvc is forbidden`))
	if !forbidden.RBACBlocked {
		t.Fatal("forbidden read must set RBACBlocked")
	}
	if forbidden.Name != "pvc" || !strings.Contains(forbidden.Reason, "RBAC") {
		t.Fatalf("forbidden skip reason must mention RBAC, got %#v", forbidden)
	}
	generic := rbacAwareSkip("pvc", errors.New("connection refused"))
	if generic.RBACBlocked {
		t.Fatal("non-forbidden read must not set RBACBlocked")
	}
	if generic.Reason != "connection refused" {
		t.Fatalf("generic skip must keep the raw reason, got %q", generic.Reason)
	}
}
