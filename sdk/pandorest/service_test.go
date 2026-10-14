package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/pandorest"
	"github.com/katbyte/pandorest/config"
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

func TestUntypedIDs(t *testing.T) {
	t.Parallel()

	got := untypedIDs("libraryId")
	if want := []string{"PlaybackSessionForClient .libraryId", "SessionSyncLocalRequest .libraryId"}; !slices.Equal(got, want) {
		t.Errorf("untypedIDs(libraryId) = %q, want %q", got, want)
	}

	if got := untypedIDs("authorId"); got != nil {
		t.Errorf("untypedIDs(authorId) = %q, want none", got)
	}
}
