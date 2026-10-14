package main

import (
	"testing"
)

func TestReadModels(t *testing.T) {
	t.Parallel()

	e := fakeProgram(t)
	lib, folder := e.models.byName["Library"], e.models.byName["folder"]
	if lib == nil || folder == nil || e.models.byName["library"] != lib || e.models.byName["Folder"] != folder {
		t.Fatalf("records by class and by model name: Library %v, folder %v", lib, folder)
	}

	cases := []struct {
		model    *model
		column   string
		kind     kind
		nullable bool
	}{
		{lib, "id", stringKind, false},
		{lib, "name", stringKind, true},
		{lib, "displayOrder", integerKind, false},
		{lib, "settings", unknownKind, false},
		{lib, "isPublic", booleanKind, true},
		{lib, "lastScan", dateKind, true},
		{lib, "createdAt", dateKind, false},
		{folder, "path", stringKind, true},
		{folder, "libraryId", stringKind, true},
		{folder, "mainLibraryId", stringKind, true},
	}
	for _, c := range cases {
		col := c.model.column(c.column)
		if col == nil {
			t.Errorf("%s.%s: no column", c.model.class.name, c.column)
			continue
		}
		if col.kind != c.kind || col.nullable != c.nullable {
			t.Errorf("%s.%s: kind %d nullable %v, want %d %v", c.model.class.name, c.column, col.kind, col.nullable, c.kind, c.nullable)
		}
	}

	if folder.column("createdAt") != nil {
		t.Error("a record with timestamps: false has createdAt")
	}

	if a, ok := lib.assocs["folders"]; !ok || !a.many || a.target != folder {
		t.Errorf("Library.folders: %+v %v, want many folders", a, ok)
	}
	if a, ok := folder.assocs["library"]; !ok || a.many || a.target != lib {
		t.Errorf("Folder.library: %+v %v, want one library", a, ok)
	}
	if a, ok := lib.assocs["mainFolder"]; !ok || a.many || a.target != folder {
		t.Errorf("Library.mainFolder, named with as: %+v %v, want one folder", a, ok)
	}
}

func TestPlural(t *testing.T) {
	t.Parallel()

	cases := map[string]string{"library": "libraries", "series": "series", "bookSeries": "bookSeries", "mediaProgress": "mediaProgresses", "author": "authors", "key": "keys"}
	for in, want := range cases {
		if got := plural(in); got != want {
			t.Errorf("plural(%q) = %q, want %q", in, got, want)
		}
	}
}
