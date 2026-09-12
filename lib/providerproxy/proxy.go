// Package providerproxy is a record/replay HTTP proxy for the metadata
// providers Audiobookshelf calls out to.
//
// abs-mcp never talks to Audible, Audnexus or iTunes itself: it asks
// Audiobookshelf to, and Audiobookshelf (a Node app) makes those calls. That
// puts them out of reach of anything that hooks Go's http.RoundTripper, so
// go-vcr and friends cannot see them. The only layer that can is a proxy in
// front of the container.
//
// Audiobookshelf uses axios, which honours HTTPS_PROXY, so pointing it here
// plus NODE_TLS_REJECT_UNAUTHORIZED=0 (safe: the container is a throwaway)
// routes every provider call through this process. In record mode the real
// providers are called once and the responses are written to cassettes; in
// replay mode - the default, and what CI uses - they are served from disk and
// no network is touched.
package providerproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Mode selects whether the proxy calls the real providers.
type Mode int

const (
	// Replay serves from the cassettes and never reaches the network. A
	// request with no recording is a loud failure, not an empty response.
	Replay Mode = iota
	// Record calls the real provider and writes what comes back.
	Record
	// Verify calls the real provider and compares the shape of what comes
	// back against the cassette, without writing. The recorded response is
	// still what gets served, so a test outcome never depends on what a
	// provider happened to return today - drift is reported separately.
	Verify
)

// Proxy is a MITM HTTP proxy backed by cassettes.
type Proxy struct {
	mode     Mode
	store    *store
	listener net.Listener
	srv      *http.Server
	logger   *log.Logger

	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	certMu sync.Mutex
	certs  map[string]*tls.Certificate

	// upstream is used in Record mode only
	upstream *http.Transport

	missMu sync.Mutex
	misses []string

	driftMu sync.Mutex
	drifts  []Drift
}

// Options configure a Proxy.
type Options struct {
	// Mode defaults to Replay.
	Mode Mode
	// CassetteDir holds one JSON file per provider host.
	CassetteDir string
	// Addr to listen on. Must be reachable from the container, so bind all
	// interfaces (e.g. "0.0.0.0:18080").
	Addr string
	// Logger receives replay misses and record notices; defaults to stderr.
	Logger *log.Logger
}

// New starts a proxy and returns it. Close stops it and, in Record mode,
// flushes the cassettes.
func New(opts Options) (*Proxy, error) {
	if opts.CassetteDir == "" {
		return nil, errors.New("providerproxy: CassetteDir is required")
	}
	if opts.Addr == "" {
		opts.Addr = "0.0.0.0:0"
	}
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, "providerproxy: ", 0)
	}

	st, err := newStore(opts.CassetteDir)
	if err != nil {
		return nil, err
	}

	ca, caKey, err := newCA()
	if err != nil {
		return nil, err
	}

	p := &Proxy{
		mode:   opts.Mode,
		store:  st,
		logger: opts.Logger,
		ca:     ca,
		caKey:  caKey,
		certs:  map[string]*tls.Certificate{},
		upstream: &http.Transport{
			Proxy:                 nil, // go straight out; we are the proxy
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	p.listener = ln
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.serve),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.Printf("serve: %v", err)
		}
	}()

	return p, nil
}

// Addr is the address the proxy is listening on.
func (p *Proxy) Addr() string { return p.listener.Addr().String() }

// Port is the port the proxy is listening on.
func (p *Proxy) Port() int { return p.listener.Addr().(*net.TCPAddr).Port }

// Misses returns the requests that had no recording, so a replay run can fail
// with the list rather than leaving tests to pass on empty responses.
func (p *Proxy) Misses() []string {
	p.missMu.Lock()
	defer p.missMu.Unlock()

	return append([]string(nil), p.misses...)
}

// Close stops the proxy, writing any newly recorded cassettes.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)

	if p.mode == Record {
		return p.store.flush()
	}

	return nil
}

// serve handles both a CONNECT tunnel (https, which is everything the
// providers use) and a plain proxied request.
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.respond(w, r, r.Host)
}

// tunnel answers CONNECT, then terminates TLS itself with a certificate minted
// for the requested host, and serves the requests inside.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	raw, _, err := hj.Hijack()
	if err != nil {
		p.logger.Printf("hijack: %v", err)
		return
	}
	defer func() { _ = raw.Close() }()

	if _, err := raw.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	cert, err := p.certFor(host)
	if err != nil {
		p.logger.Printf("cert for %s: %v", host, err)
		return
	}
	conn := tls.Server(raw, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err := conn.Handshake(); err != nil {
		// the client hung up or refused our certificate; with
		// NODE_TLS_REJECT_UNAUTHORIZED=0 the latter should not happen
		return
	}
	defer func() { _ = conn.Close() }()

	// serve every request on the tunnel until the peer closes it
	for {
		if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			return
		}
		req, err := http.ReadRequest(newReader(conn))
		if err != nil {
			return // EOF or timeout: the tunnel is done
		}
		rec := &connResponse{conn: conn}
		p.respond(rec, req, host)
		if rec.closed || req.Close {
			return
		}
	}
}

// respond serves one request from the cassettes, recording it first when in
// Record mode.
func (p *Proxy) respond(w http.ResponseWriter, r *http.Request, host string) {
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
	}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	k := key(r.Method, host, path, r.URL.Query())

	if i, ok := p.store.lookup(k); ok {
		if p.mode == Verify {
			live, err := p.fetch(r, host, k, path)
			if err != nil {
				p.logger.Printf("verify %s: %v", k, err)
			} else {
				p.compare(i, live)
			}
		}
		writeInteraction(w, i)
		return
	}

	if p.mode == Replay || p.mode == Verify {
		p.missMu.Lock()
		p.misses = append(p.misses, k)
		p.missMu.Unlock()
		p.logger.Printf("REPLAY MISS %s (run `make record` to capture it)", k)
		http.Error(w, "providerproxy: no recording for "+k, http.StatusBadGateway)
		return
	}

	i, err := p.record(r, host, k, path)
	if err != nil {
		p.logger.Printf("record %s: %v", k, err)
		http.Error(w, "providerproxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	p.logger.Printf("recorded %s -> %d", k, i.Status)
	writeInteraction(w, i)
}

// record calls the real provider and stores what it returns.
func (p *Proxy) fetch(r *http.Request, host, k, path string) (*interaction, error) {
	target := &url.URL{Scheme: "https", Host: host, Path: path, RawQuery: r.URL.RawQuery}
	if r.TLS == nil && r.URL.Scheme == "http" {
		target.Scheme = "http"
	}

	outReq, err := http.NewRequestWithContext(context.Background(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, err
	}
	for name, vals := range r.Header {
		if strings.EqualFold(name, "Proxy-Connection") {
			continue
		}
		for _, v := range vals {
			outReq.Header.Add(name, v)
		}
	}
	outReq.Host = host

	resp, err := p.upstream.RoundTrip(outReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	i := &interaction{
		Key:     k,
		Method:  strings.ToUpper(r.Method),
		Host:    strings.ToLower(host),
		Path:    path,
		Query:   r.URL.RawQuery,
		Status:  resp.StatusCode,
		Headers: keepHeaders(resp.Header),
	}
	i.setBody(body, resp.Header.Get("Content-Type"))

	return i, nil
}

// record fetches and stores. fetch alone is what Verify uses, so that a
// verification run never writes to the cassettes.
func (p *Proxy) record(r *http.Request, host, k, path string) (*interaction, error) {
	i, err := p.fetch(r, host, k, path)
	if err != nil {
		return nil, err
	}
	p.store.put(i.Host, i)

	return i, nil
}

func writeInteraction(w http.ResponseWriter, i *interaction) {
	body := i.bytes()
	for name, v := range i.Headers {
		w.Header().Set(name, v)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(i.Status)
	_, _ = w.Write(body)
}

// newCA mints the in-memory authority that signs the per-host certificates.
func newCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "abs-mcp provider proxy CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}

	return cert, k, nil
}

// certFor mints (and caches) a leaf certificate for one provider hostname.
func (p *Proxy) certFor(host string) (*tls.Certificate, error) {
	p.certMu.Lock()
	defer p.certMu.Unlock()

	if c, ok := p.certs[host]; ok {
		return c, nil
	}

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &k.PublicKey, p.caKey)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der, p.ca.Raw}, PrivateKey: k}
	p.certs[host] = cert

	return cert, nil
}
