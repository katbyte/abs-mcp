package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSendEbookTool(r *registry) {
	client := r.client

	type sendIn struct {
		itemRef
		Device string `json:"device,omitempty" jsonschema:"the e-reader, by the name it was set up with; without it the answer lists the ones this account can send to, and nothing is sent"`
	}
	type sendOut struct {
		Item    string   `json:"item"`
		Sent    bool     `json:"sent"`
		Ebook   string   `json:"ebook,omitempty"   jsonschema:"the file sent: the book's main ebook"`
		Device  string   `json:"device,omitempty"`
		Devices []string `json:"devices,omitempty" jsonschema:"without device: the e-readers this account can send to"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_send_ebook",
		Description: "Email a book's main ebook to an e-reader, such as a Kindle or Kobo address, through the server's mail settings. Without device, lists the e-readers this account can send to. " +
			"The e-readers and the mail server are set up in the web app's settings, not here. Sends an email.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, sendOut, error) {
		it, err := resolveItem(ctx, client, in.Library, in.Item)
		if err != nil {
			return nil, sendOut{}, err
		}
		out := sendOut{Item: it.Title()}
		devices, err := client.EReaderDevices(ctx)
		if err != nil {
			return nil, sendOut{}, err
		}
		names := make([]string, 0, len(devices))
		for _, d := range devices {
			names = append(names, d.Name)
		}
		if in.Device == "" {
			out.Devices = names
			return nil, out, nil
		}

		device := ""
		for _, n := range names {
			if strings.EqualFold(n, strings.TrimSpace(in.Device)) {
				device = n
			}
		}
		switch {
		case len(names) == 0:
			return nil, sendOut{}, errors.New("this account can send to no e-reader: set one up in the web app's settings, under E-mail")
		case device == "":
			return nil, sendOut{}, fmt.Errorf("no e-reader named %q; this account can send to %s", in.Device, strings.Join(quoted(names), ", "))
		case it.IsPodcast():
			return nil, sendOut{}, errNotBook
		case it.Media.EbookFile == nil:
			return nil, sendOut{}, noMainEbook(it)
		}
		if err := client.SendEbookToDevice(ctx, it.ID, device); err != nil {
			return nil, sendOut{}, err
		}
		out.Sent, out.Ebook, out.Device = true, mainEbook(it), device
		return nil, out, nil
	})
}

// noMainEbook says why a book has nothing to send: no ebook at all, or only
// supplementary ones.
func noMainEbook(it *abs.Item) error {
	for _, f := range it.LibraryFiles {
		if f.FileType == "ebook" {
			return fmt.Errorf("%q has no main ebook, only supplementary ones: item_edit ebook makes one the main one", it.Title())
		}
	}
	return fmt.Errorf("%q has no ebook", it.Title())
}

func quoted(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}
