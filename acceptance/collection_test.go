//go:build integration

package acceptance

import (
	"testing"
)

// the whole collection lifecycle in one test, so it cleans up after itself.
func TestCollectionLifecycle(t *testing.T) {
	created := call(t, "collection_create", map[string]any{
		"library": "Fiction", "name": "Integration Collection",
		"description": "made by the integration suite",
		"items":       []any{"Foundation", "Leviathan Wakes"},
	})
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no collection id: %v", created)
	}
	t.Cleanup(func() {
		call(t, "collection_delete", map[string]any{"collection": "Integration Collection"})
	})
	if n := num(t, created["books"], "books"); n != 2 {
		t.Errorf("created with %d books, want 2", n)
	}

	// list
	var found bool
	for _, row := range rows(t, call(t, "collection_list", nil)["collections"], "collections") {
		if row["name"] == "Integration Collection" {
			found = true
		}
	}
	if !found {
		t.Error("the new collection is not in collection_list")
	}

	// get, by name rather than id
	got := call(t, "collection_get", map[string]any{"collection": "Integration Collection"})
	if items := rows(t, got["items"], "items"); len(items) != 2 {
		t.Errorf("collection_get returned %d items, want 2", len(items))
	}

	// add
	added := call(t, "collection_books_edit", map[string]any{
		"collection": "Integration Collection", "action": "add", "items": []any{"Second Foundation"},
	})
	if n := num(t, added["books"], "books"); n != 3 {
		t.Errorf("after add = %d books, want 3", n)
	}

	// remove
	removed := call(t, "collection_books_edit", map[string]any{
		"collection": "Integration Collection", "action": "remove", "items": []any{"Leviathan Wakes"},
	})
	if n := num(t, removed["books"], "books"); n != 2 {
		t.Errorf("after remove = %d books, want 2", n)
	}

	// edit
	edited := call(t, "collection_edit", map[string]any{
		"collection": "Integration Collection", "name": "Integration Collection",
		"description": "renamed description",
	})
	if edited["description"] != "renamed description" {
		t.Errorf("description = %v", edited["description"])
	}

	// the books are untouched by removal from a collection
	if item := call(t, "item_get", map[string]any{"item": "Leviathan Wakes"}); item["title"] != "Leviathan Wakes" {
		t.Error("removing from a collection should not touch the item")
	}
}

func TestCollectionUnknown(t *testing.T) {
	if msg := callErr(t, "collection_get", map[string]any{"collection": "No Such Collection"}); msg == "" {
		t.Error("an unknown collection should be an error")
	}
}

func TestCollectionBooksEditValidation(t *testing.T) {
	if msg := callErr(t, "collection_books_edit", map[string]any{
		"collection": "No Such Collection", "action": "sideways", "items": []any{"Foundation"},
	}); msg == "" {
		t.Error("an unknown action should be refused")
	}
}
