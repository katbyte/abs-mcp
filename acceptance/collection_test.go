//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// the whole collection lifecycle in one test, so it cleans up after itself.
func TestCollectionLifecycle(t *testing.T) {
	created := call(t, "collection_create", map[string]any{
		"library": "Fiction", "name": "Integration Collection",
		"description": "made by the integration suite",
		"items":       []any{"Foundation", "Leviathan Wakes"},
	})
	id := text(created["id"])
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
	added := call(t, "collection_edit", map[string]any{
		"collection": "Integration Collection", "add_items": []any{"Second Foundation"},
	})
	if n := num(t, added["books"], "books"); n != 3 {
		t.Errorf("after add = %d books, want 3", n)
	}

	// remove
	removed := call(t, "collection_edit", map[string]any{
		"collection": "Integration Collection", "remove_items": []any{"Leviathan Wakes"},
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

	// all of it in one call: a description, a book in and a book out, and
	// the answer is the collection as it then is
	both := call(t, "collection_edit", map[string]any{
		"collection": "Integration Collection", "description": "Zzyzx: three things at once",
		"add_items": []any{"Leviathan Wakes"}, "remove_items": []any{"Second Foundation"},
	})
	if both["description"] != "Zzyzx: three things at once" || num(t, both["books"], "books") != 2 ||
		!slices.Equal(strs(t, both["added"], "added"), []string{"Leviathan Wakes"}) || !slices.Equal(strs(t, both["removed"], "removed"), []string{"Second Foundation"}) {
		t.Errorf("three changes in one call = %v", both)
	}
	held := titlesIn(t, call(t, "collection_get", map[string]any{"collection": "Integration Collection"})["items"], "items")
	if !slices.Contains(held, "Leviathan Wakes") || slices.Contains(held, "Second Foundation") {
		t.Errorf("the collection holds %v, want Leviathan Wakes in and Second Foundation out", held)
	}
	// what it already holds and what it never did are said, and nothing sent
	same := call(t, "collection_edit", map[string]any{
		"collection": "Integration Collection", "add_items": []any{"Leviathan Wakes"}, "remove_items": []any{"Second Foundation"},
	})
	if !slices.Equal(strs(t, same["already_held"], "already_held"), []string{"Leviathan Wakes"}) || !slices.Equal(strs(t, same["not_held"], "not_held"), []string{"Second Foundation"}) || same["added"] != nil || same["removed"] != nil {
		t.Errorf("the same change again = %v", same)
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

func TestCollectionEditValidation(t *testing.T) {
	if msg := callErr(t, "collection_edit", map[string]any{
		"collection": "No Such Collection", "add_items": []any{"Foundation"},
	}); msg == "" {
		t.Error("an unknown collection should be refused")
	}
	if msg := callErr(t, "collection_edit", map[string]any{"collection": "No Such Collection"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an edit with nothing to change: %s", msg)
	}
}
