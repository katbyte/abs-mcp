package version

import "testing"

// A build must never report a blank version. The -X flag has been given an
// empty value before, which is not the same as not being given at all.
func TestVersionIsNeverBlank(t *testing.T) {
	t.Parallel()

	if Version == "" {
		t.Error("Version is blank; the build stamped an empty -X value")
	}
}
