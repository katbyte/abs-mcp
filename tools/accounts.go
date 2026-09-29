package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Accounts: making one and changing one. The server's account routes take
// what they are sent loosely - an unknown permission or one that is not true
// or false is logged and dropped, a new account is made inactive unless told
// otherwise, the root account's type is left as it was whatever is asked,
// and a new library list is ignored unless it comes with permissions - so
// every change is read back and held to what was asked.

// accountFields are what user_create and user_edit both set.
type accountFields struct {
	Type          string   `json:"type,omitempty"            jsonschema:"user, guest or admin; a new account is a user unless this says otherwise"`
	Email         string   `json:"email,omitempty"`
	Password      string   `json:"password,omitempty"        jsonschema:"for user_create, default sixteen random letters and digits, given back once in the answer; for user_edit, a new password, or new to have one made"`
	Libraries     []string `json:"libraries,omitempty"       jsonschema:"the only libraries it can open, by name or id; all for every library. A new account opens every library"`
	Tags          []string `json:"tags,omitempty"            jsonschema:"it sees only books carrying one of these tags"`
	DeniedTags    []string `json:"denied_tags,omitempty"     jsonschema:"it sees every book except those carrying one of these tags"`
	AllTags       bool     `json:"all_tags,omitempty"        jsonschema:"user_edit: it sees books whatever their tags again, undoing tags or denied_tags"`
	Explicit      *bool    `json:"explicit,omitempty"        jsonschema:"it sees books marked explicit; a new account does if it is an admin"`
	CanDownload   *bool    `json:"can_download,omitempty"    jsonschema:"a new account can"`
	CanUpdate     *bool    `json:"can_update,omitempty"      jsonschema:"edit books' details; a new account can if it is an admin"`
	CanDelete     *bool    `json:"can_delete,omitempty"      jsonschema:"delete books and files; a new account cannot, an admin included"`
	CanUpload     *bool    `json:"can_upload,omitempty"      jsonschema:"a new account can if it is an admin"`
	CanUseEreader *bool    `json:"can_use_ereader,omitempty" jsonschema:"set up e-readers of its own to send ebooks to; a new account can if it is an admin"`
}

type accountOut struct {
	userDetail
	Password string `json:"password,omitempty" jsonschema:"the password made for it: shown this once, and never again"`
}

var accountTypes = []string{"user", "guest", "admin"}

// newPassword is sixteen random letters and digits.
func newPassword() string {
	const letters = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails; crypto/rand panics rather than return an error
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

// permissions is the part of an account change that the server keeps in its
// permissions object, and what each should read back as.
func (f accountFields) permissions() map[string]bool {
	out := map[string]bool{}
	for key, v := range map[string]*bool{
		"download": f.CanDownload, "update": f.CanUpdate, "delete": f.CanDelete, "upload": f.CanUpload,
		"createEreader": f.CanUseEreader, "accessExplicitContent": f.Explicit,
	} {
		if v != nil {
			out[key] = *v
		}
	}
	switch {
	case len(f.Tags) > 0:
		out["accessAllTags"], out["selectedTagsNotAccessible"] = false, false
	case len(f.DeniedTags) > 0:
		out["accessAllTags"], out["selectedTagsNotAccessible"] = false, true
	case f.AllTags:
		out["accessAllTags"] = true
	}
	return out
}

// check refuses what cannot be asked for, before anything is sent.
func (f accountFields) check() error {
	switch {
	case f.Type != "" && !slices.Contains(accountTypes, f.Type):
		return fmt.Errorf("type %q: user, guest or admin", f.Type)
	case len(f.Tags) > 0 && len(f.DeniedTags) > 0:
		return errors.New("tags lets it see only those books and denied_tags every book but those: one or the other")
	case f.AllTags && len(f.Tags)+len(f.DeniedTags) > 0:
		return errors.New("all_tags undoes tags and denied_tags: one or the other")
	}
	return nil
}

// libraryIDs resolves library names or ids to ids; nil with all for every
// library.
func libraryIDs(ctx context.Context, client *abs.Client, refs []string) (ids []string, all bool, err error) {
	switch {
	case len(refs) == 0:
		return nil, false, nil
	case len(refs) == 1 && strings.EqualFold(refs[0], "all"):
		return nil, true, nil
	}
	libs, err := client.Libraries(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, ref := range refs {
		i := slices.IndexFunc(libs, func(l abs.Library) bool { return l.ID == ref || strings.EqualFold(l.Name, ref) })
		if i < 0 {
			names := make([]string, 0, len(libs))
			for _, l := range libs {
				names = append(names, l.Name)
			}
			return nil, false, fmt.Errorf("no library %q (have: %s; or all)", ref, strings.Join(names, ", "))
		}
		if !slices.Contains(ids, libs[i].ID) {
			ids = append(ids, libs[i].ID)
		}
	}
	return ids, false, nil
}

// heldTo says what of an account change the server did not keep.
func heldTo(u *abs.User, f accountFields, libs []string, allLibs bool) error {
	var off []string
	have := map[string]bool{
		"download": u.Permissions.Download, "update": u.Permissions.Update, "delete": u.Permissions.Delete,
		"upload": u.Permissions.Upload, "createEreader": u.Permissions.CreateEreader,
		"accessExplicitContent": u.Permissions.AccessExplicitContent, "accessAllTags": u.Permissions.AccessAllTags,
		"selectedTagsNotAccessible": u.Permissions.SelectedTagsNotAccessible, "accessAllLibraries": u.Permissions.AccessAllLibraries,
	}
	for key, want := range f.permissions() {
		if have[key] != want {
			off = append(off, fmt.Sprintf("%s is %t", key, have[key]))
		}
	}
	if f.Type != "" && u.Type != f.Type {
		off = append(off, "the type is "+u.Type)
	}
	if f.Email != "" && u.Email != f.Email {
		off = append(off, "the email is "+u.Email)
	}
	if len(libs) > 0 && (u.Permissions.AccessAllLibraries || !sameMembers(u.LibrariesAccessible, libs)) {
		off = append(off, "the libraries it opens are not the ones asked for")
	}
	if allLibs && !u.Permissions.AccessAllLibraries {
		off = append(off, "it does not open every library")
	}
	if tags := orTags(f.Tags, f.DeniedTags); len(tags) > 0 && !sameMembers(u.ItemTagsSelected, tags) {
		off = append(off, "its tags are "+strings.Join(u.ItemTagsSelected, ", "))
	}
	if len(off) == 0 {
		return nil
	}
	slices.Sort(off)
	return fmt.Errorf("the server did not keep all of it: %s", strings.Join(off, "; "))
}

// orTags is the tag list given, tags or denied_tags.
func orTags(a, b []string) []string {
	if len(a) > 0 {
		return a
	}
	return b
}

// sameMembers reports whether two lists hold the same values, in any order.
func sameMembers(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func registerAccountTools(r *registry) {
	client := r.client

	type createIn struct {
		Username string `json:"username"`
		accountFields
	}
	add(r, writeTool, &mcp.Tool{
		Name: "user_create",
		Description: "Make an account for someone to sign in with: a user unless type says otherwise, active, opening every library unless libraries names the only ones. " +
			"Without a password one is made and given back once. The answer is the account as user_get shows it, read back from the server. Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, accountOut, error) {
		switch {
		case strings.TrimSpace(in.Username) == "":
			return nil, accountOut{}, errors.New("username is required")
		case in.AllTags:
			return nil, accountOut{}, errors.New("a new account sees books whatever their tags unless tags or denied_tags says otherwise; all_tags is for user_edit")
		case strings.EqualFold(in.Password, "new"):
			return nil, accountOut{}, errors.New(`"new" is for user_edit: leave password out to have one made`)
		}
		if err := in.check(); err != nil {
			return nil, accountOut{}, err
		}
		libs, allLibs, err := libraryIDs(ctx, client, in.Libraries)
		if err != nil {
			return nil, accountOut{}, err
		}

		out := accountOut{}
		password := in.Password
		if password == "" {
			password = newPassword()
			out.Password = password
		}
		req := abs.UserCreate{
			Username: strings.TrimSpace(in.Username), Password: password, Type: in.Type, Email: in.Email,
			IsActive: new(true), Permissions: in.permissions(), ItemTagsSelected: orTags(in.Tags, in.DeniedTags),
		}
		if len(libs) > 0 {
			req.Permissions["accessAllLibraries"], req.LibrariesAccessible = false, libs
		}
		u, err := client.CreateUser(ctx, req)
		if err != nil {
			return nil, accountOut{}, err
		}
		if err := heldTo(u, in.accountFields, libs, allLibs); err != nil {
			return nil, accountOut{}, fmt.Errorf("%s was made, but %w", u.Username, err)
		}
		if !u.IsActive {
			return nil, accountOut{}, fmt.Errorf("%s was made, but is not active: user_edit active", u.Username)
		}
		out.userDetail = describeUser(u, false)
		return nil, out, nil
	})

	type editIn struct {
		userRef
		Username string `json:"username,omitempty" jsonschema:"a new username; the account is signed out of its apps"`
		Active   *bool  `json:"active,omitempty"   jsonschema:"false stops it signing in, keeping everything; true lets it again"`
		accountFields
	}
	add(r, writeTool, &mcp.Tool{
		Name: "user_edit",
		Description: "Change an account: its username, password, type, email, whether it can sign in, the libraries it opens, the tags it sees, and what it may do. Only what is given changes. A new username or password signs it out of its apps. " +
			"The answer is the account read back from the server, and anything the server did not keep is an error: the root account's type never changes. Admin only. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, accountOut, error) {
		if strings.TrimSpace(in.User) == "" {
			return nil, accountOut{}, errors.New("user is required: the account to change, by username or id")
		}
		if err := in.check(); err != nil {
			return nil, accountOut{}, err
		}
		u, self, err := resolveUser(ctx, client, in.User)
		if err != nil {
			return nil, accountOut{}, err
		}
		switch {
		case u.Type == "root" && in.Type != "":
			return nil, accountOut{}, fmt.Errorf("%s is the root account, whose type never changes", u.Username)
		// the tools would lose the account they act through
		case self && in.Active != nil && !*in.Active:
			return nil, accountOut{}, fmt.Errorf("the API key acts as %s: stopping it signing in would lock the key out too", u.Username)
		case self && in.Type != "" && in.Type != u.Type:
			return nil, accountOut{}, fmt.Errorf("the API key acts as %s: changing its type changes what the key may do; do it in the web app", u.Username)
		}
		libs, allLibs, err := libraryIDs(ctx, client, in.Libraries)
		if err != nil {
			return nil, accountOut{}, err
		}

		out := accountOut{}
		upd := abs.UserUpdate{Type: strPtr(in.Type), Email: strPtr(in.Email), IsActive: in.Active, Permissions: in.permissions()}
		if name := strings.TrimSpace(in.Username); name != "" && name != u.Username {
			upd.Username = &name
		}
		switch {
		case strings.EqualFold(in.Password, "new"):
			p := newPassword()
			upd.Password, out.Password = &p, p
		case in.Password != "":
			upd.Password = &in.Password
		}
		// the server takes a library list only beside permissions
		switch {
		case allLibs:
			upd.Permissions["accessAllLibraries"] = true
		case len(libs) > 0:
			upd.Permissions["accessAllLibraries"], upd.LibrariesAccessible = false, libs
		}
		if tags := orTags(in.Tags, in.DeniedTags); len(tags) > 0 {
			upd.ItemTagsSelected = tags
		}
		if upd.Username == nil && upd.Password == nil && upd.Type == nil && upd.Email == nil && upd.IsActive == nil && len(upd.Permissions) == 0 && len(upd.ItemTagsSelected) == 0 {
			return nil, accountOut{}, errors.New("nothing to change: pass at least one field")
		}

		got, err := client.UpdateUser(ctx, u.ID, upd)
		if err != nil {
			return nil, accountOut{}, err
		}
		if err := heldTo(got, in.accountFields, libs, allLibs); err != nil {
			return nil, accountOut{}, fmt.Errorf("%s was changed, but %w", got.Username, err)
		}
		switch {
		case upd.Username != nil && got.Username != *upd.Username:
			return nil, accountOut{}, fmt.Errorf("the server kept the username %s", got.Username)
		case in.Active != nil && got.IsActive != *in.Active:
			return nil, accountOut{}, fmt.Errorf("%s was changed, but is still %s", got.Username, map[bool]string{true: "active", false: "inactive"}[got.IsActive])
		}
		out.userDetail = describeUser(got, self)
		return nil, out, nil
	})
}
