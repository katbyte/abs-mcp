//go:build integration

// Package integration covers every tool against a real Audiobookshelf running
// in Docker - the API wrappers and the audits alike - so response shapes,
// filter encoding and permissions are checked against the thing abs-mcp
// actually talks to rather than a stub.
//
// Fixtures are built through the tools themselves (library_create,
// library_scan, item_edit), so the setup is part of the coverage.
//
//	make testacc                       # start a container, run these, tear it down
//
//	eval "$(scripts/abs-testenv.sh up)"  # or drive it by hand
//	go test -tags integration ./integration/...
//	scripts/abs-testenv.sh down
package acceptance

import "testing"

func TestMain(m *testing.M) { testMain(m) }
