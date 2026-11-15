package api

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/activity"
	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/extract"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

// domainSentinels names every exported sentinel of the service packages that
// reach the HTTP layer. TestEveryDomainErrorIsMapped checks it against the
// source, so a new sentinel must be added here, and then to errorKinds.
var domainSentinels = map[string]error{
	"activity.ErrInvalidCursor":      activity.ErrInvalidCursor,
	"audit.ErrInvalidCursor":         audit.ErrInvalidCursor,
	"auth.ErrInvalidCredentials":     auth.ErrInvalidCredentials,
	"auth.ErrNoSession":              auth.ErrNoSession,
	"authz.ErrNotFound":              authz.ErrNotFound,
	"authz.ErrForbidden":             authz.ErrForbidden,
	"authz.ErrBadSignature":          authz.ErrBadSignature,
	"comments.ErrInvalid":            comments.ErrInvalid,
	"comments.ErrInvalidCursor":      comments.ErrInvalidCursor,
	"credentials.ErrInvalid":         credentials.ErrInvalid,
	"credentials.ErrNotFound":        credentials.ErrNotFound,
	"credentials.ErrProjectNotFound": credentials.ErrProjectNotFound,
	"credentials.ErrLabelTooLong":    credentials.ErrLabelTooLong,
	"delivery.ErrInvalid":            delivery.ErrInvalid,
	"delivery.ErrNotFound":           delivery.ErrNotFound,
	"delivery.ErrRateLimited":        delivery.ErrRateLimited,
	"extract.ErrBadPattern":          extract.ErrBadPattern,
	"groups.ErrNotFound":             groups.ErrNotFound,
	"groups.ErrNameTaken":            groups.ErrNameTaken,
	"groups.ErrInvalid":              groups.ErrInvalid,
	"ingest.ErrProjectNotFound":      ingest.ErrProjectNotFound,
	"messages.ErrNotFound":           messages.ErrNotFound,
	"messages.ErrInvalidCursor":      messages.ErrInvalidCursor,
	"messages.ErrInvalidRun":         messages.ErrInvalidRun,
	"messages.ErrEmptyFilter":        messages.ErrEmptyFilter,
	"messages.ErrTagNotFound":        messages.ErrTagNotFound,
	"messages.ErrTagExists":          messages.ErrTagExists,
	"messages.ErrInvalidTag":         messages.ErrInvalidTag,
	"policy.ErrAddress":              policy.ErrAddress,
	"policy.ErrAllowlist":            policy.ErrAllowlist,
	"policy.ErrBlocked":              policy.ErrBlocked,
	"projects.ErrNotFound":           projects.ErrNotFound,
	"projects.ErrSlugTaken":          projects.ErrSlugTaken,
	"projects.ErrInvalid":            projects.ErrInvalid,
	"search.ErrTooManyTerms":         search.ErrTooManyTerms,
	"search.ErrSizeTooLarge":         search.ErrSizeTooLarge,
	"search.ErrInvalidDate":          search.ErrInvalidDate,
	"search.ErrQueryTooLong":         search.ErrQueryTooLong,
	"tokens.ErrInvalid":              tokens.ErrInvalid,
	"tokens.ErrNotFound":             tokens.ErrNotFound,
	"tokens.ErrBadInput":             tokens.ErrBadInput,
	"users.ErrNotFound":              users.ErrNotFound,
	"users.ErrEmailTaken":            users.ErrEmailTaken,
	"users.ErrInvalid":               users.ErrInvalid,
	"users.ErrLastAdmin":             users.ErrLastAdmin,
	"users.ErrWrongPassword":         users.ErrWrongPassword,
}

// exportedSentinels lists the exported package level Err* variables declared
// in the non test files of internal/<pkg>, as "pkg.ErrName".
func exportedSentinels(t *testing.T, pkg string) []string {
	t.Helper()
	dir := filepath.Join("..", pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					if strings.HasPrefix(id.Name, "Err") && id.IsExported() {
						out = append(out, pkg+"."+id.Name)
					}
				}
			}
		}
	}
	return out
}

// TestEveryDomainErrorIsMapped fails when a service package declares an
// exported Err* sentinel that the shared error table does not map to a
// client error, which would otherwise reach clients as a logged 500.
func TestEveryDomainErrorIsMapped(t *testing.T) {
	pkgs := map[string]bool{}
	for name := range domainSentinels {
		pkgs[strings.SplitN(name, ".", 2)[0]] = true
	}
	var unlisted []string
	for pkg := range pkgs {
		for _, name := range exportedSentinels(t, pkg) {
			if _, ok := domainSentinels[name]; !ok {
				unlisted = append(unlisted, name)
			}
		}
	}
	slices.Sort(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("sentinels missing from domainSentinels (and probably errorKinds): %v", unlisted)
	}
	for name, err := range domainSentinels {
		t.Run(name, func(t *testing.T) {
			wrapped := fmt.Errorf("context: %w", err)
			if s := classify(wrapped).status(); s >= http.StatusInternalServerError {
				t.Errorf("%s maps to %d", name, s)
			}
		})
	}
}

func TestFailAndCompatFailShareTheTable(t *testing.T) {
	s := &server{Deps: Deps{Log: slog.New(slog.DiscardHandler)}}
	cases := []struct {
		err    error
		status int
	}{
		{messages.ErrNotFound, http.StatusNotFound},
		{users.ErrNotFound, http.StatusNotFound},
		{authz.ErrForbidden, http.StatusForbidden},
		{users.ErrEmailTaken, http.StatusConflict},
		{search.ErrTooManyTerms, http.StatusBadRequest},
		{messages.ErrInvalidRun, http.StatusBadRequest},
		{fmt.Errorf("x: %w", policy.ErrBlocked), http.StatusBadRequest},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			native, compat := httptest.NewRecorder(), httptest.NewRecorder()
			s.fail(native, r, tc.err)
			s.compatFail(compat, r, tc.err)
			if native.Code != tc.status || compat.Code != tc.status {
				t.Errorf("native %d, compat %d, want %d", native.Code, compat.Code, tc.status)
			}
			if tc.status == http.StatusInternalServerError &&
				(strings.Contains(native.Body.String(), "boom") || strings.Contains(compat.Body.String(), "boom")) {
				t.Error("internal error text leaked")
			}
		})
	}
}
