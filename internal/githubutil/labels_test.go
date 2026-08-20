package githubutil

import "testing"

func TestJobMatchesRequiresAllLabels(t *testing.T) {
	t.Parallel()
	runner := []string{"self-hosted", "Linux", "X64", "gh-runnerd"}
	if !JobMatches(runner, []string{"gh-runnerd"}) {
		t.Fatal("expected match on custom label")
	}
	if !JobMatches(runner, []string{"self-hosted", "gh-runnerd"}) {
		t.Fatal("expected match on subset")
	}
	if JobMatches(runner, []string{"gh-runnerd", "gpu"}) {
		t.Fatal("must not match missing gpu label")
	}
	if JobMatches(runner, nil) {
		t.Fatal("empty job labels must not match")
	}
}

func TestOwnsJob(t *testing.T) {
	t.Parallel()
	if !OwnsJob([]string{"gh-runnerd"}, []string{"self-hosted", "gh-runnerd"}) {
		t.Fatal("should own")
	}
	if OwnsJob([]string{"gh-runnerd"}, []string{"ubuntu-latest"}) {
		t.Fatal("should not own hosted label")
	}
}

func TestParseLabelList(t *testing.T) {
	t.Parallel()
	got := ParseLabelList(" gh-runnerd, kvm, gh-runnerd ,")
	if len(got) != 2 || got[0] != "gh-runnerd" || got[1] != "kvm" {
		t.Fatalf("got %v", got)
	}
	if ParseLabelList("  , ") != nil {
		t.Fatal("empty input should yield no labels")
	}
}

func TestMergeLabels(t *testing.T) {
	t.Parallel()
	got := MergeLabels([]string{"gh-runnerd"}, []string{"gh-runnerd", "linux"})
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
