//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"testing"
)

// keepAudioFiles puts a seeded book's files back as they were once the test
// is done, and rescans it. An embed writes tags into the audio itself, which
// no tool takes out again: left in, a second run of the suite on the same
// server found the fixture already tagged, and a copy of the file carried the
// book's title into another test.
func keepAudioFiles(t *testing.T, item string) {
	t.Helper()

	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	got := call(t, "item_get", map[string]any{"item": item})
	id, _ := got["id"].(string)
	folder, _ := got["full_path"].(string)
	if id == "" || folder == "" {
		t.Fatalf("item_get %q has no id or folder: %v", item, got)
	}
	// the container mounts each library folder at the root, under the name
	// it has under ABS_TEST_DATA
	dir := filepath.Join(data, folder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	saved := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a fixture folder this suite laid out
		if err != nil {
			t.Fatal(err)
		}
		saved[e.Name()] = b
	}

	t.Cleanup(func() {
		for name, b := range saved {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o666); err != nil { //nolint:gosec // the fixtures are shared with the container's user
				t.Errorf("putting %s back: %v", name, err)
			}
		}
		call(t, "item_rescan", map[string]any{"item": id})
	})
}
