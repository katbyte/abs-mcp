package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/pandorest"
	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/generator"
	"github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// repoRoot is where the service's paths resolve from.
const repoRoot = "../.."

// loadDocument reads a fresh copy of a service's document under api-defs, the highest version there is.
func loadDocument(service string) (*openapi.Spec, error) {
	svc, _ := config.Find(services, service)
	svc, err := svc.In(repoRoot).Resolve()
	if err != nil {
		return nil, err
	}

	return openapi.Load(svc.Path(svc.Spec))
}

// Every fix applies to the document as it stands, and would notice the day it no longer needs to.
func TestFixes(t *testing.T) {
	t.Parallel()

	if err := workarounds.Verify(fixes, loadDocument); err != nil {
		t.Fatal(err)
	}
}

// The checked-in definitions are what a fresh import of the document gives, so a changed document, fix or id kind is imported before it
// is committed. The import refuses an id kind that reaches nothing or claims a place another does.
func TestDefinitionsCurrent(t *testing.T) {
	t.Parallel()

	var out, log bytes.Buffer
	err := pandorest.Run(generation(), []string{"diff", "-root", repoRoot, "-quiet", "-exit-code"}, &out, &log)
	if err != nil || !strings.Contains(out.String(), serviceName+": no changes") {
		t.Errorf("diff against the document = %v (run make pandorest-import):\n%s%s", err, out.String(), log.String())
	}
}

// The generated package is what a fresh import and generation of the document give, file for file, and has a method for every operation, so a
// changed document or setting is generated before it is committed: make gencheck, as a test. The files written by hand beside it are left out.
func TestGeneratedCurrent(t *testing.T) {
	t.Parallel()

	listed, _ := config.Find(services, serviceName)
	svc, err := listed.In(repoRoot).Resolve()
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	doc, err := os.ReadFile(svc.Path(svc.Spec))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, svc.Spec)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, svc.Spec), doc, 0o600); err != nil { //nolint:gosec // the service's own document path under the test's temp dir
		t.Fatal(err)
	}

	for _, cmd := range []string{"import", "generate"} {
		var out, log bytes.Buffer
		if err := pandorest.Run(generation(), []string{cmd, "-root", root}, &out, &log); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, log.String())
		}
	}
	for _, dir := range []string{svc.Definitions, svc.Output} {
		want, got := listFiles(t, filepath.Join(repoRoot, dir), dir == svc.Output), listFiles(t, filepath.Join(root, dir), false)
		if len(got) != len(want) {
			t.Errorf("%s: %d files generated, %d checked in; run make generate", dir, len(got), len(want))
		}
		for name, content := range want {
			if got[name] != content {
				t.Errorf("%s/%s differs from a fresh generation; run make generate", dir, name)
			}
		}
	}

	var out, log bytes.Buffer
	if err := pandorest.Run(generation(), []string{"check", "-root", repoRoot, "-quiet"}, &out, &log); err != nil || !strings.Contains(out.String(), "every one has a method in") {
		t.Errorf("check = %v:\n%s%s", err, out.String(), log.String())
	}
}

// listFiles reads a folder's files, leaving out the folders in it, such as the answers saved beside the definitions; generatedOnly leaves out
// the files written by hand beside generated code too.
func listFiles(t *testing.T, dir string, generatedOnly bool) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a folder under test
		if err != nil {
			t.Fatal(err)
		}
		if generatedOnly && !generator.Generated(b) {
			continue
		}
		out[e.Name()] = string(b)
	}

	return out
}

func TestUntypedIDs(t *testing.T) {
	t.Parallel()

	got := untypedIDs("libraryId", "userId")
	if want := []string{"ApiKeyUpdateRequest .userId", "PlaylistUpdateRequest .libraryId", "PlaylistUpdateRequest .userId", "SessionSyncLocalRequest .libraryId", "SessionSyncLocalRequest .userId"}; !slices.Equal(got, want) {
		t.Errorf("untypedIDs(libraryId, userId) = %q, want %q", got, want)
	}

	if got := untypedIDs("authorId"); got != nil {
		t.Errorf("untypedIDs(authorId) = %q, want none", got)
	}
}
