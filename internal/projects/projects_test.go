package projects_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"My App":                 "my-app",
		"  Hello,   World!  ":    "hello-world",
		"Café Étoile":            "caf-toile",
		"---":                    "project",
		"":                       "project",
		"日本語":                    "project",
		"a_b.c":                  "a-b-c",
		strings.Repeat("ab", 50): strings.Repeat("ab", 31) + "a",
	}
	for in, want := range tests {
		if got := projects.Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestProjectsLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := projects.New(testutil.Pool(t))

	p, err := svc.Create(ctx, "  Billing API ", "")
	if err != nil || p.Slug != "billing-api" || p.Name != "Billing API" {
		t.Fatalf("create: %+v %v", p, err)
	}
	if _, err := svc.Create(ctx, "Other", "billing-api"); !errors.Is(err, projects.ErrSlugTaken) {
		t.Errorf("duplicate slug: %v", err)
	}
	for _, c := range [][2]string{{"", ""}, {strings.Repeat("x", 201), ""}, {"ok", "Bad Slug"}, {"ok", "-lead"}} {
		if _, err := svc.Create(ctx, c[0], c[1]); !errors.Is(err, projects.ErrInvalid) {
			t.Errorf("%v: %v", c, err)
		}
	}
	got, err := svc.Get(ctx, authz.Trusted(p.ID))
	if err != nil || got.ID != p.ID {
		t.Errorf("get: %+v %v", got, err)
	}
	if _, err := svc.Get(ctx, authz.Trusted(9999)); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := svc.Create(ctx, "Alpha", ""); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Alpha" {
		t.Errorf("list: %+v %v", list, err)
	}
}

func TestTagRules(t *testing.T) {
	ctx := context.Background()
	svc := projects.New(testutil.Pool(t))
	p, _ := svc.Create(ctx, "P", "")
	other, _ := svc.Create(ctx, "Q", "")

	rule, err := svc.CreateTagRule(ctx, authz.Trusted(p.ID), projects.TagRuleInput{Kind: ingest.RuleHeader, HeaderName: " X-Run-Id ", Tag: "run:{value}"})
	if err != nil || rule.HeaderName != "X-Run-Id" {
		t.Fatalf("create: %+v %v", rule, err)
	}
	if _, err := svc.CreateTagRule(ctx, authz.Trusted(p.ID), projects.TagRuleInput{Kind: ingest.RuleRecipient, Pattern: "*@x.test", Tag: "x"}); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]projects.TagRuleInput{
		"unknown kind":          {Kind: "other", Tag: "t"},
		"header without name":   {Kind: ingest.RuleHeader, Tag: "t"},
		"recipient with header": {Kind: ingest.RuleRecipient, HeaderName: "X", Tag: "t"},
		"empty tag":             {Kind: ingest.RuleRecipient, Tag: " "},
		"long tag":              {Kind: ingest.RuleRecipient, Tag: strings.Repeat("t", 101)},
		"long pattern":          {Kind: ingest.RuleRecipient, Pattern: strings.Repeat("p", 201), Tag: "t"},
	} {
		if _, err := svc.CreateTagRule(ctx, authz.Trusted(p.ID), in); !errors.Is(err, projects.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.CreateTagRule(ctx, authz.Trusted(9999), projects.TagRuleInput{Kind: ingest.RuleRecipient, Tag: "t"}); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}

	rules, err := svc.ListTagRules(ctx, authz.Trusted(p.ID))
	if err != nil || len(rules) != 2 {
		t.Fatalf("list: %+v %v", rules, err)
	}
	if l, _ := svc.ListTagRules(ctx, authz.Trusted(other.ID)); len(l) != 0 {
		t.Error("rules leaked across projects")
	}
	if err := svc.DeleteTagRule(ctx, authz.Trusted(other.ID), rule.ID); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("delete from another project: %v", err)
	}
	if err := svc.DeleteTagRule(ctx, authz.Trusted(p.ID), rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTagRule(ctx, authz.Trusted(p.ID), rule.ID); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	svc := projects.New(pool)
	pool.Close()
	if _, err := svc.Create(ctx, "x", ""); err == nil || errors.Is(err, projects.ErrSlugTaken) {
		t.Errorf("create: %v", err)
	}
	if _, err := svc.Get(ctx, authz.Trusted(1)); err == nil || errors.Is(err, projects.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := svc.List(ctx); err == nil {
		t.Error("list")
	}
	if _, err := svc.ListTagRules(ctx, authz.Trusted(1)); err == nil {
		t.Error("list rules")
	}
	if _, err := svc.CreateTagRule(ctx, authz.Trusted(1), projects.TagRuleInput{Kind: ingest.RuleRecipient, Tag: "t"}); err == nil {
		t.Error("create rule")
	}
	if err := svc.DeleteTagRule(ctx, authz.Trusted(1), 1); err == nil {
		t.Error("delete rule")
	}
}

// TestListVisibleRanksOneRowPerProject is table-driven over group setups: a
// user sees only projects they have a group on, once, with the highest rank.
func TestListVisibleRanksOneRowPerProject(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	svc := projects.New(pool)
	user := testutil.User(t, pool, "u@example.test", false)
	other := testutil.User(t, pool, "o@example.test", false)

	tests := []struct {
		slug  string
		roles []string // one group per role, the user is in all of them
		want  int32    // 0 means not listed
	}{
		{"alpha", []string{"viewer"}, 1},
		{"bravo", []string{"member"}, 2},
		{"charlie", []string{"viewer", "owner", "member"}, 3},
		{"delta", nil, 0},
	}
	for _, tc := range tests {
		pid := testutil.Project(t, pool, tc.slug)
		for i, role := range tc.roles {
			g := testutil.Group(t, pool, tc.slug+"-"+role+"-"+string(rune('a'+i)))
			testutil.Assign(t, pool, pid, g, role)
			testutil.Member(t, pool, g, user)
		}
	}
	// A group the user is not in must not leak a role.
	pid := testutil.Project(t, pool, "echo")
	g := testutil.Group(t, pool, "echo-owner")
	testutil.Assign(t, pool, pid, g, "owner")
	testutil.Member(t, pool, g, other)

	got, err := svc.ListVisible(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	ranks := map[string]int32{}
	for _, v := range got {
		if _, dup := ranks[v.Project.Slug]; dup {
			t.Errorf("%s listed twice", v.Project.Slug)
		}
		ranks[v.Project.Slug] = v.Rank
	}
	for _, tc := range tests {
		if ranks[tc.slug] != tc.want {
			t.Errorf("%s: rank %d, want %d", tc.slug, ranks[tc.slug], tc.want)
		}
	}
	if _, ok := ranks["echo"]; ok {
		t.Error("a project of another user's group is listed")
	}
}
