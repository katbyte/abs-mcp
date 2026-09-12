//go:build integration

package acceptance

import (
	"testing"
)

// audit_terminology_rename is the only write in the audit family: it merges a
// group audit_terminology found. The fixtures are deliberately consistent, so
// each case here introduces the divergence, merges it away, and puts the
// fixture back the way it found it.

func TestAuditTerminologyRenameLanguage(t *testing.T) {
	const item = "Abaddon's Gate"

	// "eng" and "English" are the same language spelled two ways
	call(t, "item_edit", map[string]any{"item": item, "language": "eng"})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": item, "language": "English"})
	})

	// which audit_terminology now groups
	groups := rows(t, call(t, "audit_terminology", map[string]any{"field": "languages"})["groups"], "groups")
	if len(groups) != 1 {
		t.Fatalf("audit_terminology found %d language groups, want 1: %v", len(groups), groups)
	}

	out := call(t, "audit_terminology_rename", map[string]any{
		"library": "Fiction", "field": "languages", "from": "eng", "to": "English",
	})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	if titles := strs(t, out["items"], "items"); len(titles) != 1 || titles[0] != item {
		t.Errorf("items = %v, want [%s]", titles, item)
	}

	// and the group is gone
	if after := rows(t, call(t, "audit_terminology", map[string]any{"field": "languages"})["groups"], "groups"); len(after) != 0 {
		t.Errorf("audit_terminology still reports %d groups after the merge: %v", len(after), after)
	}
}

func TestAuditTerminologyRenamePublisher(t *testing.T) {
	const item = "War Is a Racket"

	call(t, "item_edit", map[string]any{"item": item, "publisher": "round table"})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": item, "publisher": "Round Table"})
	})

	out := call(t, "audit_terminology_rename", map[string]any{
		"field": "publishers", "from": "round table", "to": "Round Table",
	})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}

	// the item really carries the kept spelling now
	if got := call(t, "item_get", map[string]any{"item": item})["publisher"]; got != "Round Table" {
		t.Errorf("publisher = %v, want Round Table", got)
	}
}

// the fields with a dedicated rename tool are refused here rather than half
// handled, and so is a value nothing carries.
func TestAuditTerminologyRenameRefusals(t *testing.T) {
	for _, field := range []string{"narrators", "tags", "genres", "authors", "nope"} {
		args := map[string]any{"field": field, "from": "a", "to": "b"}
		if msg := callErr(t, "audit_terminology_rename", args); msg == "" {
			t.Errorf("field %q should be refused", field)
		}
	}

	for _, args := range []map[string]any{
		{"field": "languages", "from": "", "to": "English"},
		{"field": "languages", "from": "English", "to": ""},
	} {
		if msg := callErr(t, "audit_terminology_rename", args); msg == "" {
			t.Errorf("%v should be refused: from and to are both required", args)
		}
	}

	if msg := callErr(t, "audit_terminology_rename", map[string]any{
		"field": "languages", "from": "Old High Martian", "to": "English",
	}); msg == "" {
		t.Error("renaming a language nothing carries should be an error")
	}
}
