package tasks

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

type browserNetwork struct {
	scope       *browserScope
	dir         string
	guard       string
	resourceKey string
	proxy       *httputil.ReverseProxy
	dial        *http.Client
}

func startBrowserNetwork(ctx context.Context, scope *browserScope, dir, guard, resourceKey string, ignoreUpstreamTLS bool) (string, string, func(), error) {
	certificate, pin, err := browserTLSCertificate()
	if err != nil {
		return "", "", nil, err
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		// The host's explicit ignore-https-errors setting still applies upstream.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: ignoreUpstreamTLS},
	}
	network := &browserNetwork{scope: scope, dir: dir, guard: guard, resourceKey: resourceKey, dial: &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	network.proxy = &httputil.ReverseProxy{
		Transport:     transport,
		BufferPool:    &browserResponseBuffers{},
		FlushInterval: browserResponseBatchDelay,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.Host = request.Out.URL.Host
			request.Out.Header.Del("Proxy-Authorization")
		},
		ModifyResponse: network.filterResponse,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if errors.Is(err, errBrowserScopeChanged) {
				panic(http.ErrAbortHandler)
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	// Tunnels terminate locally so dedicated workers, frames and native
	// WebSockets use the same request checks as the main document.
	tlsAddr, stopTLS, err := serveBrowserHTTP(ctx, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.IsAbs() || req.Method == http.MethodConnect {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req.URL.Scheme, req.URL.Host = "https", req.Host
		network.serve(w, req)
	}), &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return "", "", nil, err
	}
	addr, stopHTTP, err := serveBrowserHTTP(ctx, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodConnect {
			_, origin := browserDestination("https://" + req.Host)
			if origin == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			tunnelBrowserTLS(ctx, w, req, tlsAddr)
			return
		}
		if !req.URL.IsAbs() || req.URL.Scheme != "http" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		network.serve(w, req)
	}), nil)
	if err != nil {
		stopTLS()
		return "", "", nil, err
	}
	return addr, pin, func() {
		stopHTTP()
		stopTLS()
		transport.CloseIdleConnections()
	}, nil
}

func serveBrowserHTTP(ctx context.Context, handler http.Handler, config *tls.Config) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	address := listener.Addr().String()
	if config != nil {
		listener = tls.NewListener(listener, config)
	} else {
		address = "http://" + address
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	go server.Serve(listener)
	return address, func() { _ = server.Close() }, nil
}

func browserTLSCertificate() (tls.Certificate, string, error) {
	// This key only lives for this managed browser session. Chromium trusts its
	// public key pin through an isolated launch profile.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Buddy managed browsing"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	cert, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(publicKey)
	return tls.Certificate{Certificate: [][]byte{cert}, PrivateKey: key}, base64.StdEncoding.EncodeToString(sum[:]), nil
}

func tunnelBrowserTLS(ctx context.Context, w http.ResponseWriter, req *http.Request, addr string) {
	dest, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(req.Context(), "tcp", addr)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer dest.Close()
	client, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { _ = client.Close(); _ = dest.Close() })
	defer stop()
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || buffered.Flush() != nil {
		return
	}
	go func() {
		_, _ = io.Copy(dest, buffered)
		_ = dest.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(client, dest)
}

func (n *browserNetwork) current(ctx context.Context) (AutomationControl, string) {
	control, err := readAutomationControlFile(n.dir)
	if err != nil || control.Paused {
		return control, ""
	}
	return control, currentAutomationEpoch(ctx, n.guard, control)
}

const browserResourceHeader = "X-Buddy-Resource"

type browserResponseKey struct{}

type browserResponseScope struct {
	epoch  string
	cancel context.CancelFunc
}

var errBrowserScopeChanged = errors.New("browser authorization changed")

func (n *browserNetwork) serve(w http.ResponseWriter, req *http.Request) {
	address := req.URL.String()
	_, origin := browserDestination(address)
	control, epoch := n.current(req.Context())
	n.scope.noteHistoryEpoch(epoch)
	if origin == "" || epoch == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	webSocket := strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
	if req.Header.Get("Upgrade") != "" && !webSocket {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	mode := req.Header.Get("Sec-Fetch-Mode")
	resource := mode == "cors" || mode == "no-cors" || mode == "same-origin" ||
		mode == "" && req.Header.Get(browserResourceHeader) == n.resourceKey
	req.Header.Del(browserResourceHeader)
	allowed := false
	var submission *browserSubmission
	if safeBrowserMethod(req.Method) {
		// HTTP on a LAN can omit fetch metadata. Require a trustworthy
		// subresource classification before using an origin-wide grant.
		if !webSocket && !resource {
			allowed = n.scope.checkDestination(req.Context(), n.dir, n.guard, address, control, true)
		} else {
			allowed = n.scope.originInEpoch(origin, epoch)
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(req.Body, maxBrowserRequestBody+1))
		opaque := err != nil || len(data) > maxBrowserRequestBody || !utf8.Valid(data)
		submission = n.scope.checkSubmission(req.Context(), n.dir, n.guard, browserRequest{
			URL: address, Method: req.Method, ContentType: req.Header.Get("Content-Type"), Body: string(data),
		}, control, epoch, opaque)
		if submission != nil {
			allowed = true
			defer submission.cancel()
			defer submission.finish()
			req = req.WithContext(submission.ctx)
			req.Body = io.NopCloser(bytes.NewReader(data))
		}
	}
	if !allowed {
		// Fail the browser load without turning a denial into a page response.
		panic(http.ErrAbortHandler)
	}
	if latest, current := n.current(req.Context()); latest != control || current != epoch {
		if submission != nil && submission.ctx.Err() != nil {
			// A terminal response also keeps Chromium from retrying a
			// cancelled submission after a connection close.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		panic(http.ErrAbortHandler)
	}
	n.scope.recordNetwork(epoch, req.Method, address)
	if webSocket {
		n.serveSocket(w, req, control, epoch)
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	ctx = context.WithValue(ctx, browserResponseKey{}, browserResponseScope{epoch, cancel})
	if submission != nil {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					submission.finish()
				}
			},
		})
	}
	n.proxy.ServeHTTP(w, req.WithContext(ctx))
}

func (n *browserNetwork) filterResponse(response *http.Response) error {
	ctx := response.Request.Context()
	scope, _ := ctx.Value(browserResponseKey{}).(browserResponseScope)
	_, current := n.current(ctx)
	if scope.epoch == "" || scope.epoch != current {
		return errBrowserScopeChanged
	}
	response.Header.Set("Cache-Control", "no-store")
	body := response.Body
	streamingEvents := strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
	if !streamingEvents && (response.ContentLength < 0 || response.ContentLength >= browserResponseBatch) {
		body = newBrowserBatchReader(body, scope.cancel)
	}
	response.Body = &browserResponseBody{ReadCloser: body, valid: func() bool {
		_, current := n.current(ctx)
		return current == scope.epoch
	}}
	return nil
}

const browserResponseBatch = 512 << 10

type browserResponseBuffers struct {
	pool sync.Pool
}

func (b *browserResponseBuffers) Get() []byte {
	if buf := b.pool.Get(); buf != nil {
		return *buf.(*[]byte)
	}
	return make([]byte, browserResponseBatch)
}

func (b *browserResponseBuffers) Put(buf []byte) {
	b.pool.Put(&buf)
}

type browserResponseBody struct {
	io.ReadCloser
	valid func() bool
}

func (body *browserResponseBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if n > 0 && !body.valid() {
		return 0, errBrowserScopeChanged
	}
	return n, err
}

func (n *browserNetwork) serveSocket(w http.ResponseWriter, req *http.Request, control AutomationControl, epoch string) {
	address := *req.URL
	address.Scheme = "ws"
	if req.URL.Scheme == "https" {
		address.Scheme = "wss"
	}
	headers := req.Header.Clone()
	protocols := strings.FieldsFunc(headers.Get("Sec-WebSocket-Protocol"), func(r rune) bool { return r == ',' || r == ' ' })
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), "sec-websocket-") || key == "Upgrade" || key == "Connection" || key == "Proxy-Authorization" {
			headers.Del(key)
		}
	}
	upstream, response, err := websocket.Dial(req.Context(), address.String(), &websocket.DialOptions{
		HTTPClient: n.dial, HTTPHeader: headers, Subprotocols: protocols,
	})
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer upstream.CloseNow()
	if _, current := n.current(req.Context()); current != epoch {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	for _, cookie := range response.Header.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", cookie)
	}
	client, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		// The upstream received and validated the browser's original Origin.
		InsecureSkipVerify: true, Subprotocols: []string{upstream.Subprotocol()},
	})
	if err != nil {
		return
	}
	defer client.CloseNow()
	client.SetReadLimit(maxBrowserRequestBody)
	upstream.SetReadLimit(MaxWorkArtifactBytes)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	go func() {
		defer cancel()
		n.forwardSocket(ctx, client, upstream, address, control, epoch)
	}()
	n.forwardSocket(ctx, upstream, client, url.URL{}, control, epoch)
}

func (n *browserNetwork) forwardSocket(ctx context.Context, from, to *websocket.Conn, destination url.URL, control AutomationControl, epoch string) {
	for {
		kind, data, err := from.Read(ctx)
		if err != nil {
			if code := websocket.CloseStatus(err); code != -1 && code != websocket.StatusNoStatusRcvd {
				_ = to.Close(code, "")
			}
			return
		}
		if _, current := n.current(ctx); current != epoch {
			return
		}
		if destination.Host != "" {
			submission := n.scope.checkSubmission(ctx, n.dir, n.guard, browserRequest{
				URL: destination.String(), Method: "SEND", Body: string(data),
			}, control, epoch, !utf8.Valid(data))
			if submission == nil {
				continue
			}
			_, current := n.current(submission.ctx)
			ok := current == epoch && to.Write(submission.ctx, kind, data) == nil
			submission.finish()
			submission.cancel()
			if !ok {
				return
			}
		} else if to.Write(ctx, kind, data) != nil {
			return
		}
	}
}
