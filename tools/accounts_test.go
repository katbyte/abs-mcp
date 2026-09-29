package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// user_create sends an active account with the permissions asked for and the
// libraries named, makes a password when none is given, and reads the
// account back; the server makes one inactive unless told.
func TestUserCreate(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("POST /api/users", `{"user":{"id":"`+readerID+`","username":"reader","type":"user","isActive":true,"librariesAccessible":["`+libID+`"],`+
		`"permissions":{"download":true,"accessAllLibraries":false,"accessAllTags":true,"accessExplicitContent":true}}}`)
	call := toolCaller(t, f)

	out, err := call("user_create", map[string]any{"username": "reader", "libraries": []any{"books"}, "explicit": true})
	if err != nil {
		t.Fatal(err)
	}
	if pw := str(t, out["password"]); len(pw) != 16 {
		t.Errorf("password = %q, want sixteen characters made for it", pw)
	}
	if out["username"] != "reader" || !isTrue(out["active"]) || boolOf(t, out["all_libraries"]) {
		t.Errorf("answer = %v", out)
	}

	sent := f.requests("/api/users")
	if len(sent) != 1 {
		t.Fatalf("sent %d times", len(sent))
	}
	var body struct {
		Username            string
		Password            string
		IsActive            *bool
		Permissions         map[string]bool
		LibrariesAccessible []string
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.IsActive == nil || !*body.IsActive || body.Password != str(t, out["password"]) ||
		body.Permissions["accessAllLibraries"] || !body.Permissions["accessExplicitContent"] || len(body.LibrariesAccessible) != 1 || body.LibrariesAccessible[0] != libID {
		t.Errorf("sent %s", sent[0].Body)
	}
}

// What the server did not keep is an error naming it, and what cannot be
// asked for is refused before anything is sent.
func TestUserCreateChecks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	call := toolCaller(t, f)

	for _, c := range []struct {
		args map[string]any
		says []string
	}{
		{map[string]any{"username": " "}, []string{"username is required"}},
		{map[string]any{"username": "r", "type": "owner"}, []string{`type "owner"`}},
		{map[string]any{"username": "r", "tags": []any{"a"}, "denied_tags": []any{"b"}}, []string{"one or the other"}},
		{map[string]any{"username": "r", "all_tags": true}, []string{"all_tags is for user_edit"}},
		{map[string]any{"username": "r", "libraries": []any{"Films"}}, []string{`no library "Films"`, "Books"}},
	} {
		_, err := call("user_create", c.args)
		wantErr(t, fmt.Sprint(c.args), err, c.says...)
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}

	// the server drops a permission it does not like, and says nothing
	f.json("POST /api/users", `{"user":{"id":"`+readerID+`","username":"reader","type":"user","isActive":true,"permissions":{"accessAllLibraries":true,"delete":false}}}`)
	_, err := call("user_create", map[string]any{"username": "reader", "password": "pw", "can_delete": true})
	wantErr(t, "a permission not kept", err, "reader was made, but", "delete is false")
}

// user_edit sends only what is given, the libraries beside a permissions
// object the server needs to take them, and holds the answer to it.
func TestUserEdit(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	accounts(f)
	oneLibrary(f)
	f.json("PATCH /api/users/"+readerID, `{"success":true,"user":{"id":"`+readerID+`","username":"reader","type":"user","isActive":true,`+
		`"librariesAccessible":["`+libID+`"],"permissions":{"accessAllLibraries":false,"upload":true}}}`)
	call := toolCaller(t, f)

	out, err := call("user_edit", map[string]any{"user": "reader", "libraries": []any{libID}, "can_upload": true, "password": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(str(t, out["password"])) != 16 || boolOf(t, out["all_libraries"]) {
		t.Errorf("answer = %v", out)
	}
	sent := f.requests("/api/users/" + readerID)
	var patch []string
	for _, r := range sent {
		if r.Method == http.MethodPatch {
			patch = append(patch, r.Body)
		}
	}
	if len(patch) != 1 || !strings.Contains(patch[0], `"accessAllLibraries":false`) || !strings.Contains(patch[0], `"upload":true`) ||
		!strings.Contains(patch[0], `"librariesAccessible":["`+libID+`"]`) || strings.Contains(patch[0], `"type"`) || strings.Contains(patch[0], `"email"`) {
		t.Errorf("sent %v", patch)
	}

	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{"user": "reader"}, "nothing to change"},
		{map[string]any{}, "user is required"},
		{map[string]any{"user": "kt", "type": "user"}, "root account"},
		{map[string]any{"user": "kt", "active": false}, "lock the key out"},
	} {
		_, err := call("user_edit", c.args)
		wantErr(t, fmt.Sprint(c.args), err, c.says)
	}
}
