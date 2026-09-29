package tools

import (
	"slices"
	"testing"
)

// Without a device the e-readers this account can send to are listed and
// nothing is sent; with one, the book's main ebook goes to it by its own name.
func TestItemSendEbook(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, `{"id":"`+itemID+`","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"Dune"},`+
		`"ebookFile":{"ino":"e1","metadata":{"filename":"Dune.epub","relPath":"Dune.epub"}}}}`)
	f.json("POST /api/authorize", `{"user":{},"ereaderDevices":[{"name":"Kobo Libra","email":"kt@kobo.test"},{"name":"Kindle","email":"kt@kindle.test"}]}`)
	f.json("POST /api/emails/send-ebook-to-device", `OK`)
	call := toolCaller(t, f)

	out, err := call("item_send_ebook", map[string]any{"item": itemID})
	if err != nil {
		t.Fatal(err)
	}
	if got := strs(t, out["devices"]); !slices.Equal(got, []string{"Kobo Libra", "Kindle"}) || isTrue(out["sent"]) {
		t.Errorf("list = %v", out)
	}
	if got := f.requests("/api/emails/send-ebook-to-device"); len(got) != 0 {
		t.Errorf("sent %v", got)
	}

	out, err = call("item_send_ebook", map[string]any{"item": itemID, "device": "kobo libra"})
	if err != nil {
		t.Fatal(err)
	}
	if !isTrue(out["sent"]) || out["ebook"] != "Dune.epub" || out["device"] != "Kobo Libra" {
		t.Errorf("send = %v", out)
	}
	sent := f.requests("/api/emails/send-ebook-to-device")
	if len(sent) != 1 || sent[0].Body != `{"deviceName":"Kobo Libra","libraryItemId":"`+itemID+`"}` {
		t.Errorf("sent %v", sent)
	}

	_, err = call("item_send_ebook", map[string]any{"item": itemID, "device": "Nook"})
	wantErr(t, "an unknown device", err, `no e-reader named "Nook"`, `"Kindle"`)
}

// A book with only supplementary ebooks has nothing the server would send.
func TestItemSendEbookNeedsAMainEbook(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, `{"id":"`+itemID+`","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"Dune"}},`+
		`"libraryFiles":[{"ino":"e2","fileType":"ebook","isSupplementary":true,"metadata":{"filename":"Dune.pdf"}}]}`)
	f.json("POST /api/authorize", `{"ereaderDevices":[{"name":"Kindle"}]}`)
	call := toolCaller(t, f)

	_, err := call("item_send_ebook", map[string]any{"item": itemID, "device": "Kindle"})
	wantErr(t, "no main ebook", err, "no main ebook", "item_edit ebook")
	if got := f.changes(); len(got) != 1 || got[0].Path != "/api/authorize" {
		t.Errorf("sent %v", got)
	}
}
