package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A handler that panics answers its call with an error naming the tool and
// logs the stack, rather than ending the session: the next call is served.
func TestAPanicInAToolIsAnErrorNotACrash(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		logged strings.Builder
	)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	r := &registry{server: srv, client: newTestClient(t), errorLog: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(&logged, format, args...)
	}}
	type none struct{}
	add(r, writeTool, &mcp.Tool{Name: "zzyzx_panic"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		var found map[string]*abs.Library

		return nil, none{}, errors.New(found["9"].ID) // a nil dereference
	})
	add(r, readTool, &mcp.Tool{Name: "zzyzx_fine"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})
	for _, p := range r.pending {
		p.register()
	}

	st, ct := mcp.NewInMemoryTransports()
	ctx := t.Context()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "zzyzx_panic", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("the panic reached the session: %v", err)
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	msg := strings.Join(texts, "; ")
	if !res.IsError || !strings.Contains(msg, "internal error in zzyzx_panic") || !strings.Contains(msg, "nil pointer") {
		t.Errorf("the answer = %q (error %v), want the tool and the panic named", msg, res.IsError)
	}
	mu.Lock()
	log := logged.String()
	mu.Unlock()
	if !strings.Contains(log, "internal error in zzyzx_panic") || !strings.Contains(log, "goroutine") {
		t.Errorf("the log = %q, want the panic and its stack", log)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "zzyzx_fine", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("the call after the panic failed: %v %+v", err, res)
	}
}
