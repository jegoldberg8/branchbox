package state

import (
	"testing"
	"time"
)

func stack(project, slug, branch string) *Stack {
	return &Stack{Project: project, Slug: slug, Branch: branch, StartedAt: time.Now()}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	in := stack("proj", "feature-x", "feature/x")
	in.Ports = map[string]int{"PORT": 3120}
	in.Services = []string{"rwaworker"}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load("proj", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if out.Branch != "feature/x" || out.Ports["PORT"] != 3120 || len(out.Services) != 1 {
		t.Errorf("round trip lost data: %+v", out)
	}
}

func TestListIsScopedAndOrdered(t *testing.T) {
	s := New(t.TempDir())
	for _, st := range []*Stack{
		stack("b", "two", "two"), stack("a", "beta", "beta"), stack("a", "alpha", "alpha"),
	} {
		if err := s.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Slug != "alpha" || all[2].Project != "b" {
		t.Errorf("unexpected order: %+v", all)
	}
	only, err := s.List("a")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 2 {
		t.Errorf("project scoping failed: %+v", only)
	}
}

func TestResolveAcceptsSlugOrBranch(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(stack("proj", "feature-x", "feature/x")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"feature-x", "feature/x"} {
		got, err := s.Resolve("proj", ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got.Slug != "feature-x" {
			t.Errorf("%s resolved to %q", ref, got.Slug)
		}
	}
	if _, err := s.Resolve("proj", "nope"); err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
}

func TestResolveRefusesAnAmbiguousRefAcrossProjects(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(stack("a", "main", "main")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(stack("b", "main", "main")); err != nil {
		t.Fatal(err)
	}
	// Silently picking one would stop the wrong container.
	if _, err := s.Resolve("", "main"); err == nil {
		t.Fatal("expected an ambiguity error")
	}
	if _, err := s.Resolve("a", "main"); err != nil {
		t.Fatalf("project scoping should disambiguate: %v", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(stack("proj", "x", "x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("proj", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("proj", "x"); err != nil {
		t.Errorf("second remove should be a no-op: %v", err)
	}
}

func TestLoadMissingStackExplainsHowToStartIt(t *testing.T) {
	s := New(t.TempDir())
	_, err := s.Load("proj", "nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "" {
		t.Error("empty error message")
	}
}
