//go:build integration

// The binary, not the package. Everything else in this suite drives
// tools.RegisterAll in process, which is every line of tool code the binary
// runs; what it never touches is the thin layer around it - stdio framing,
// the flags and environment reaching the server, the HTTP routes, and what a
// bad start says. Those break silently: a stray line on stdout corrupts the
// protocol and a client simply fails to connect, with every other test green.
//
// So this is a smoke test: the real binary, built from this checkout, spoken
// to the way a client speaks to it, against the same seeded server.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	binOnce sync.Once
	binPath string
	binDir  string
	binErr  error
)

// removeBinary deletes the binary the smoke test built, at the end of the
// run: it is built once for the suite, so no one test can clean it up.
func removeBinary() {
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
}

// binary builds abs-mcp from this checkout, once per run, so the test speaks
// to the code in the tree rather than to whatever is installed.
func binary(t *testing.T) string {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	binOnce.Do(func() {
		binDir, binErr = os.MkdirTemp("", "abs-mcp-binary")
		if binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "abs-mcp")
		out, err := exec.CommandContext(ctx, "go", "build", "-o", binPath, "..").CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("building abs-mcp: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}

	return binPath
}

// serverEnv is the environment a client would give the binary: the seeded
// server and key, and nothing else of ours, so a stray ABS_* in the shell
// running the tests cannot change what is registered. HOME is an empty
// directory, so a real ~/.abs-mcp cannot either.
func serverEnv(t *testing.T, extra ...string) []string {
	t.Helper()

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"ABS_SERVER=" + os.Getenv("ABS_SERVER"),
		"ABS_TOKEN=" + os.Getenv("ABS_TOKEN"),
	}

	return append(env, extra...)
}

// syncBuffer collects output a process writes while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// pipe is a reader that also closes the process's stdout.
type teeReadCloser struct {
	io.Reader
	io.Closer
}

// stdio starts "abs-mcp serve" as a client does, connects an MCP session over
// its stdin and stdout, and keeps everything the process wrote to each stream
// so a test can assert on them. dir is the working directory, for the config
// file lookup.
func stdio(t *testing.T, dir string, env []string, args ...string) (*mcp.ClientSession, *syncBuffer, *syncBuffer) {
	t.Helper()

	cmd := exec.CommandContext(context.WithoutCancel(ctx), binary(t), append([]string{"serve"}, args...)...)
	cmd.Env = env
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, errOut := &syncBuffer{}, &syncBuffer{}
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	session, err := mcp.NewClient(&mcp.Implementation{Name: "binary-test", Version: "0"}, nil).Connect(ctx, &mcp.IOTransport{
		Reader: teeReadCloser{Reader: io.TeeReader(stdout, out), Closer: stdout},
		Writer: stdin,
	}, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("connecting to the binary over stdio: %v\nstderr: %s", err, errOut)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	return session, out, errOut
}

// toolNamesOf lists the tools a session's server registered.
func toolNamesOf(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)

	return names
}

// librariesThrough calls a read-only tool over the session and returns the
// library names, which proves the server url and key reached the client the
// binary built.
func librariesThrough(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "library_list"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("library_list through the binary: %v", res.Content)
	}
	out, _ := res.StructuredContent.(map[string]any)
	var names []string
	for _, l := range rows(t, out["libraries"], "libraries") {
		names = append(names, text(l["name"]))
	}
	slices.Sort(names)

	return names
}

// MCP over stdio uses stdout for the protocol: one line of anything else -
// a print, a log line, a cobra message - corrupts the stream, and the client
// fails to connect with every test still green. So the binary is run at the
// most verbose log level and every line it writes to stdout is checked.
func TestBinaryStdioSpeaksOnlyTheProtocol(t *testing.T) {
	session, out, errOut := stdio(t, t.TempDir(), serverEnv(t, "ABS_LOG=trace"), "--toolsets", "all")

	if libs := librariesThrough(t, session); !slices.Contains(libs, "Fiction") {
		t.Errorf("libraries through the binary = %v", libs)
	}
	if err := session.Close(); err != nil {
		t.Errorf("closing the session: %v", err)
	}

	for i, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.JSONRPC != "2.0" {
			t.Fatalf("stdout line %d is not a protocol message, which breaks the stream: %q", i+1, line)
		}
	}
	// the logging that must not be on stdout does happen, on stderr
	if logged := errOut.String(); !strings.Contains(logged, "registered") {
		t.Errorf("stderr has no startup log line at trace level: %q", logged)
	}
}

// The flags and the environment have to reach the running server, not just
// parse: what a client lists is what was asked for.
func TestBinaryRegistersWhatTheFlagsAsk(t *testing.T) {
	for _, c := range []struct {
		name     string
		env      []string
		args     []string
		want     []string // tools that must be registered
		unwanted []string // tools that must not be
	}{
		{
			name:     "the default set is core",
			want:     []string{"library_list", "library_search", "item_get", "server_info"},
			unwanted: []string{"item_edit", "audit_all", "collection_create"},
		},
		{
			name: "a toolset from the environment",
			env:  []string{"ABS_TOOLSETS=curation"},
			want: []string{"audit_all", "item_edit", "library_list"},
		},
		{
			name:     "--read-only drops every tool that writes",
			args:     []string{"--toolsets", "all", "--read-only"},
			want:     []string{"library_list", "audit_all"},
			unwanted: []string{"item_edit", "item_delete", "collection_create", "user_progress_set"},
		},
		{
			name:     "delete tools need --enable-delete",
			args:     []string{"--toolsets", "all"},
			want:     []string{"item_edit"},
			unwanted: []string{"item_delete", "author_delete", "podcast_episode_delete"},
		},
		{
			name: "--enable-delete from the environment",
			env:  []string{"ABS_TOOLSETS=all", "ABS_ENABLE_DELETE=true"},
			want: []string{"item_delete", "author_delete"},
		},
		{
			name:     "--allow-tools narrows what a toolset registered",
			args:     []string{"--toolsets", "all", "--allow-tools", "library_*"},
			want:     []string{"library_list", "library_items"},
			unwanted: []string{"item_get", "audit_all"},
		},
		{
			name:     "--deny-tools removes from it",
			args:     []string{"--toolsets", "curation", "--deny-tools", "item_*"},
			want:     []string{"audit_all"},
			unwanted: []string{"item_edit", "item_match"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// an empty working directory: only the flags and the environment
			// under test decide what is registered
			session, _, _ := stdio(t, t.TempDir(), serverEnv(t, c.env...), c.args...)
			names := toolNamesOf(t, session)
			for _, want := range c.want {
				if !slices.Contains(names, want) {
					t.Errorf("%s is not registered, of the %d that are", want, len(names))
				}
			}
			for _, unwanted := range c.unwanted {
				if slices.Contains(names, unwanted) {
					t.Errorf("%s is registered, of the %d that are", unwanted, len(names))
				}
			}
		})
	}
}

// A .abs-mcp in the working directory is how a project configures the binary
// without putting a key in .mcp.json, and viper reads it before $HOME's.
func TestBinaryReadsTheConfigFileInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	config := fmt.Sprintf("server=%s\ntoken=%s\ntoolsets=curation\n", os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN"))
	if err := os.WriteFile(filepath.Join(dir, ".abs-mcp"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	// nothing in the environment: the file is all it has
	session, _, _ := stdio(t, dir, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()})

	if names := toolNamesOf(t, session); !slices.Contains(names, "audit_all") {
		t.Errorf("the toolset in the config file was not used: %v", names)
	}
	if libs := librariesThrough(t, session); !slices.Contains(libs, "Fiction") {
		t.Errorf("the server and token in the config file did not reach the client: %v", libs)
	}
}

// --listen has to serve the protocol at /mcp behind the bearer check, answer
// the health probe a container watches, stay up, and shut down when asked.
func TestBinaryServesHTTP(t *testing.T) {
	const token = "zzyzx-binary-token" //nolint:gosec // a test token for a throwaway server

	addr := freePort(t)
	cmd := exec.CommandContext(context.WithoutCancel(ctx), binary(t), "serve", "--listen", addr, "--auth-token", token, "--toolsets", "all")
	cmd.Env = serverEnv(t)
	errOut := &syncBuffer{}
	cmd.Stderr, cmd.Stdout = errOut, errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	base := "http://" + addr
	waitHealthy(t, base, exited, errOut)

	get := func(path, auth string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })

		return res
	}

	t.Run("the endpoint is behind the bearer check", func(t *testing.T) {
		if res := get("/mcp", ""); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /mcp with no token = %d, want 401", res.StatusCode)
		} else if !strings.Contains(res.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("no Bearer challenge: %q", res.Header.Get("WWW-Authenticate"))
		}
		if res := get("/mcp", "Bearer nope"); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /mcp with the wrong token = %d, want 401", res.StatusCode)
		}
		if res := get("/nope", "Bearer "+token); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET /nope = %d, want 404", res.StatusCode)
		}
	})

	t.Run("and serves the protocol with it", func(t *testing.T) {
		session, err := mcp.NewClient(&mcp.Implementation{Name: "binary-test", Version: "0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   base + "/mcp",
			HTTPClient: &http.Client{Transport: bearer(token)},
		}, nil)
		if err != nil {
			t.Fatalf("connecting over HTTP: %v\n%s", err, errOut)
		}
		defer func() { _ = session.Close() }()

		if names := toolNamesOf(t, session); !slices.Contains(names, "audit_all") {
			t.Errorf("tools over HTTP = %v", names)
		}
		if libs := librariesThrough(t, session); !slices.Contains(libs, "Fiction") {
			t.Errorf("libraries over HTTP = %v", libs)
		}
	})

	t.Run("and shuts down when asked", func(t *testing.T) {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("exit after SIGTERM: %v\n%s", err, errOut)
			}
		case <-time.After(shutdownGrace):
			t.Errorf("still running %s after SIGTERM", shutdownGrace)
		}
	})
}

// shutdownGrace is how long the binary gets to drain and exit.
const shutdownGrace = 15 * time.Second

// bearer adds the HTTP endpoint's token to every request.
func bearer(token string) http.RoundTripper {
	return roundTripper(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Authorization", "Bearer "+token)

		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// freePort is an address nothing is listening on, for --listen.
func freePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	return addr
}

// waitHealthy polls the health probe until the server answers it, failing
// with whatever the process said if it exits first.
func waitHealthy(t *testing.T, base string, exited <-chan error, out *syncBuffer) {
	t.Helper()

	for range 100 {
		select {
		case err := <-exited:
			t.Fatalf("the server exited before serving: %v\n%s", err, out)
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := http.DefaultClient.Do(req); err == nil {
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "ok" {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no health response from the server\n%s", out)
}

// A start that cannot work has to say so and stop. A client waiting on stdio
// sees nothing at all otherwise, and a container restarts in a loop.
func TestBinaryRefusesAStartItCannotMake(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		args []string
		says string
	}{
		{
			name: "no server",
			env:  []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "ABS_TOKEN=" + os.Getenv("ABS_TOKEN")},
			says: "server parameter can't be empty",
		},
		{
			name: "no token",
			env:  []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "ABS_SERVER=" + os.Getenv("ABS_SERVER")},
			says: "token parameter can't be empty",
		},
		{
			name: "--listen with no auth token",
			args: []string{"--listen", "127.0.0.1:0"},
			says: "--auth-token",
		},
		{
			name: "a toolset that does not exist",
			args: []string{"--toolsets", "zzyzx"},
			says: "zzyzx",
		},
		{
			name: "an allow pattern matching nothing",
			args: []string{"--allow-tools", "zzyzx_*"},
			says: "zzyzx_*",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := c.env
			if env == nil {
				env = serverEnv(t)
			}
			cmd := exec.CommandContext(context.WithoutCancel(ctx), binary(t), append([]string{"serve"}, c.args...)...)
			cmd.Env = env
			cmd.Dir = t.TempDir() // no .abs-mcp to fall back on
			out := &syncBuffer{}
			cmd.Stdout, cmd.Stderr = out, out

			done := make(chan error, 1)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			go func() { done <- cmd.Wait() }()

			select {
			case err := <-done:
				if err == nil {
					t.Errorf("exited 0, want a refusal: %s", out)
				}
			case <-time.After(shutdownGrace):
				_ = cmd.Process.Kill()
				t.Fatalf("still running: a bad start has to fail, not hang\n%s", out)
			}
			if got := out.String(); !strings.Contains(got, c.says) {
				t.Errorf("said %q, want it to name %q", got, c.says)
			}
		})
	}
}

// The commands a person runs to check an install, rather than a client.
func TestBinaryCommands(t *testing.T) {
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.WithoutCancel(ctx), binary(t), args...)
		cmd.Env = serverEnv(t)
		cmd.Dir = t.TempDir()
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}

		return string(out)
	}

	if got := run(t, "version"); !strings.HasPrefix(got, "abs-mcp ") {
		t.Errorf("version = %q", got)
	}
	// tools needs no server, and says what it would register
	if got := run(t, "tools", "-q", "--toolsets", "all", "--enable-delete"); !strings.Contains(got, "item_delete\n") {
		t.Errorf("tools -q listed %d tools, none of them item_delete", len(strings.Fields(got)))
	}
	if got := run(t, "info"); !strings.Contains(got, "audiobookshelf ") || !strings.Contains(got, "library \"Fiction\"") {
		t.Errorf("info = %q", got)
	}
}
