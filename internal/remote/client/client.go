package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var (
	ErrUnreachable   = errors.New("remote server unreachable")
	ErrCertChanged   = errors.New("pinned fingerprint mismatch")
	ErrUnknownServer = errors.New("unknown server")
	ErrVersion       = errors.New("server rejected protocol version (426)")
)

// Version is this client's own build version, sent as remote.HeaderClientVersion
// on every request (#373 §4.4). It is informational -- the server logs it and
// never signs or rejects on it -- and cmd/relevo sets it once at startup.
var Version = ""

// HTTPError represents an HTTP error response containing status and a remote.ErrorBody.
type HTTPError struct {
	Status int
	Body   remote.ErrorBody
}

func (e *HTTPError) Error() string {
	if e.Body.Message != "" {
		return e.Body.Message
	}
	return fmt.Sprintf("http %d: %s", e.Status, e.Body.Code)
}

// Client communicates with remote relevo servers using ed25519 request signing.
type Client struct {
	servers Servers
	key     remote.Keypair
	now     func() time.Time

	mu          sync.Mutex
	httpClients map[string]*http.Client
}

// New creates a new Client for the given servers, signing key, and clock.
func New(servers Servers, key remote.Keypair, now func() time.Time) *Client {
	if now == nil {
		now = time.Now
	}
	return &Client{
		servers:     servers,
		key:         key,
		now:         now,
		httpClients: make(map[string]*http.Client),
	}
}

func (c *Client) getHTTPClient(entry ServerEntry) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := fmt.Sprintf("%s|%s|%s|%v", entry.URL, entry.Fingerprint, entry.CA, entry.Insecure)
	if cl, ok := c.httpClients[key]; ok {
		return cl
	}

	var tlsConfig *tls.Config
	if entry.Insecure {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	} else if entry.CA == "system" {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("no certificates presented")
				}
				fp := fingerprintOf(rawCerts[0])
				if !strings.EqualFold(fp, entry.Fingerprint) {
					return ErrCertChanged
				}
				return nil
			},
		}
	}

	tr := &http.Transport{
		TLSClientConfig: tlsConfig,
	}
	cl := &http.Client{
		Transport: tr,
	}
	c.httpClients[key] = cl
	return cl
}

func (c *Client) doRequest(ctx context.Context, server, method, pathWithQuery string, body io.Reader, contentLength int64, bodySHA []byte, contentType string) (*http.Response, error) {
	entry, ok := c.servers[server]
	if !ok {
		return nil, ErrUnknownServer
	}

	base := strings.TrimRight(entry.URL, "/")
	fullURL := base + pathWithQuery
	u, err := url.Parse(fullURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if Version != "" {
		req.Header.Set(remote.HeaderClientVersion, Version)
	}

	nonce, err := remote.NewNonce()
	if err != nil {
		return nil, fmt.Errorf("new nonce: %w", err)
	}

	target := u.RequestURI()
	headers := remote.Sign(c.key, method, target, bodySHA, c.now(), nonce)
	for k, vv := range headers {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	httpClient := c.getHTTPClient(entry)
	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, ErrCertChanged) || strings.Contains(err.Error(), ErrCertChanged.Error()) {
			return nil, ErrCertChanged
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	if resp.StatusCode == http.StatusUpgradeRequired {
		_ = resp.Body.Close()
		return nil, ErrVersion
	}

	// A gateway status is a redeploy in progress, not an answer from relevo's
	// own handler (#373 §4.4): the 52x/530 pages are Cloudflare's HTML, so the
	// body is dropped and the error is an ErrUnreachable a retry can act on.
	if isGatewayStatus(resp.StatusCode) {
		_ = resp.Body.Close()
		text := http.StatusText(resp.StatusCode)
		if text == "" {
			text = "gateway error"
		}
		return nil, fmt.Errorf("%w: server returned %d %s", ErrUnreachable, resp.StatusCode, text)
	}

	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNoContent {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var errBody remote.ErrorBody
		if jsonErr := json.Unmarshal(bodyBytes, &errBody); jsonErr == nil && (errBody.Code != "" || errBody.Message != "") {
			return nil, &HTTPError{Status: resp.StatusCode, Body: errBody}
		}
		return nil, &HTTPError{
			Status: resp.StatusCode,
			Body: remote.ErrorBody{
				Code:    remote.Code(http.StatusText(resp.StatusCode)),
				Message: strings.TrimSpace(string(bodyBytes)),
			},
		}
	}

	return resp, nil
}

func (c *Client) do(ctx context.Context, server, method, pathWithQuery string, bodyBytes, bodySHA []byte, contentType string) (*http.Response, error) {
	var reader io.Reader
	var length int64 = -1
	if bodyBytes != nil {
		reader = bytes.NewReader(bodyBytes)
		length = int64(len(bodyBytes))
	}
	if len(bodySHA) == 0 {
		sum := sha256.Sum256(bodyBytes)
		bodySHA = sum[:]
	}
	return c.doRequest(ctx, server, method, pathWithQuery, reader, length, bodySHA, contentType)
}

// isGatewayStatus reports whether status is one a CDN gateway answers with
// while the relevo server behind it is down (#373 §4.4): 502, 503, 504, the
// Cloudflare 520-527 range and 530.
func isGatewayStatus(status int) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case 530:
		return true
	}
	return status >= 520 && status <= 527
}

const (
	// retryAttempts is how many times a retried call tries in total (#373 §4.4).
	retryAttempts = 4
	// retryBase is the first backoff; every further attempt doubles it, so the
	// sleeps are 1s, 2s and 4s before the fourth and last attempt.
	retryBase = time.Second
)

// sleep waits for d, or until ctx is done. It is a package variable so tests
// replace it rather than waiting out retry's backoff (#373 §4.4).
var sleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retry calls f until it succeeds or until attempts are used up. It retries
// only an error that wraps ErrUnreachable -- a gateway page, a dial failure, a
// per-attempt timeout; every other error is returned at once. The backoff is
// base<<i, and the last error is what the caller sees when the attempts run
// out (#373 §4.4).
func retry[T any](ctx context.Context, attempts int, base time.Duration, f func(context.Context) (T, error)) (T, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var out T
		out, err = f(ctx)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, ErrUnreachable) || i == attempts-1 {
			break
		}
		if serr := sleep(ctx, base<<uint(i)); serr != nil {
			break
		}
	}
	var zero T
	return zero, err
}

// roundFileDeadline is the per-attempt deadline for the two downloads that
// stream a body to the caller (#373 §4.4); the other calls keep their 30s. It
// is a package variable so a test can shorten it.
var roundFileDeadline = 2 * time.Minute

// deadlineBody ties a download's cancel function to its response body, so the
// per-attempt deadline stops its timer when the caller closes the body rather
// than while the caller is still reading it.
type deadlineBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (d *deadlineBody) Close() error {
	err := d.ReadCloser.Close()
	d.cancel()
	return err
}

// startRoundDeadline is StartRound's per-attempt deadline. An upload carries
// the plan and the whole bundle, so it gets the 30s every other call has plus
// 1s per 256KiB of body, capped at 10 minutes (#373 §4.4).
func startRoundDeadline(size int64) time.Duration {
	d := 30*time.Second + time.Duration(size/(256*1024))*time.Second
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// WhoAmI fetches caller identification from the server.
func (c *Client) WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (remote.WhoAmI, error) {
		resp, err := c.do(ctx, server, "GET", "/v1/whoami", nil, nil, "")
		if err != nil {
			return remote.WhoAmI{}, err
		}
		defer resp.Body.Close()

		var w remote.WhoAmI
		if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
			return remote.WhoAmI{}, fmt.Errorf("decode whoami: %w", err)
		}
		return w, nil
	})
}

// Candidates lists available builder candidates on the server.
func (c *Client) Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (remote.CandidatesResponse, error) {
		resp, err := c.do(ctx, server, "GET", "/v1/candidates", nil, nil, "")
		if err != nil {
			return remote.CandidatesResponse{}, err
		}
		defer resp.Body.Close()

		var respBody remote.CandidatesResponse
		if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
			return remote.CandidatesResponse{}, fmt.Errorf("decode candidates: %w", err)
		}
		return respBody, nil
	})
}

// CreateBinding requests the creation of a remote binding.
func (c *Client) CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	data, err := json.Marshal(req)
	if err != nil {
		return remote.BindingView{}, fmt.Errorf("marshal request: %w", err)
	}
	sum := sha256.Sum256(data)
	resp, err := c.do(ctx, server, "POST", "/v1/bindings", data, sum[:], "application/json")
	if err != nil {
		return remote.BindingView{}, err
	}
	defer resp.Body.Close()

	var view remote.BindingView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return remote.BindingView{}, fmt.Errorf("decode binding view: %w", err)
	}
	return view, nil
}

// GetBinding fetches a binding's view.
func (c *Client) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s", url.PathEscape(name))
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (remote.BindingView, error) {
		resp, err := c.do(ctx, server, "GET", path, nil, nil, "")
		if err != nil {
			return remote.BindingView{}, err
		}
		defer resp.Body.Close()

		var view remote.BindingView
		if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
			return remote.BindingView{}, fmt.Errorf("decode binding view: %w", err)
		}
		return view, nil
	})
}

// StartRound begins a round on the server, spooling the multipart form (plan,
// tags + bundle). tags is the client's tag list shipped as data beside the
// bundle (#242); nil or empty omits the field entirely. candidate is a
// canonical candidate token for the round and every later one (#318); "" omits
// the field, which leaves the binding's builder unchanged.
//
// retryOnUnreachable is the caller's answer to whether the server advertised
// remote.FeatureIdempotentSend (#373 §4.4): only then is a repeated send safe,
// and only then is a 502 retried. Every attempt re-reads the spooled temp file
// from its start, so the retried request carries the same plan and bundle
// bytes as the first.
func (c *Client) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, tags []remote.TagRef, retryOnUnreachable bool) (remote.BindingView, error) {
	tmp, err := os.CreateTemp("", "relevo-start-round-*.tmp")
	if err != nil {
		return remote.BindingView{}, fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	hasher := sha256.New()
	mw := multipart.NewWriter(io.MultiWriter(tmp, hasher))

	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		return remote.BindingView{}, fmt.Errorf("write round field: %w", err)
	}
	if err := mw.WriteField("plan", string(plan)); err != nil {
		return remote.BindingView{}, fmt.Errorf("write plan field: %w", err)
	}
	if tier != "" {
		if err := mw.WriteField("tier", tier); err != nil {
			return remote.BindingView{}, fmt.Errorf("write tier field: %w", err)
		}
	}
	if candidate != "" {
		if err := mw.WriteField("candidate", candidate); err != nil {
			return remote.BindingView{}, fmt.Errorf("write candidate field: %w", err)
		}
	}
	if len(tags) > 0 {
		encoded, err := json.Marshal(tags)
		if err != nil {
			return remote.BindingView{}, fmt.Errorf("marshal tags: %w", err)
		}
		if err := mw.WriteField("tags", string(encoded)); err != nil {
			return remote.BindingView{}, fmt.Errorf("write tags field: %w", err)
		}
	}
	if bundle != nil {
		part, err := mw.CreateFormFile("bundle", "bundle.bundle")
		if err != nil {
			return remote.BindingView{}, fmt.Errorf("create bundle form file: %w", err)
		}
		if _, err := io.Copy(part, bundle); err != nil {
			return remote.BindingView{}, fmt.Errorf("copy bundle: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return remote.BindingView{}, fmt.Errorf("close multipart writer: %w", err)
	}

	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return remote.BindingView{}, fmt.Errorf("seek end: %w", err)
	}

	bodySHA := hasher.Sum(nil)
	path := fmt.Sprintf("/v1/bindings/%s/rounds", url.PathEscape(name))

	attempt := func(ctx context.Context) (remote.BindingView, error) {
		// The retry re-reads this attempt's body from the top of the spooled
		// file; the file itself is removed once, by the defer above, after the
		// last attempt.
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return remote.BindingView{}, fmt.Errorf("seek start: %w", err)
		}
		actx, cancel := context.WithTimeout(ctx, startRoundDeadline(size))
		defer cancel()

		// LimitReader rather than tmp itself: http closes a body that is an
		// io.ReadCloser, and a retry needs the spooled file open.
		resp, err := c.doRequest(actx, server, "POST", path, io.LimitReader(tmp, size), size, bodySHA, mw.FormDataContentType())
		if err != nil {
			return remote.BindingView{}, err
		}
		defer resp.Body.Close()

		var view remote.BindingView
		if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
			return remote.BindingView{}, fmt.Errorf("decode binding view: %w", err)
		}
		return view, nil
	}

	if retryOnUnreachable {
		return retry(ctx, retryAttempts, retryBase, attempt)
	}
	return attempt(ctx)
}

// RoundFile streams a file (report, diff, log) for a given round.
func (c *Client) RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/files/%s", url.PathEscape(name), round, url.PathEscape(kind))
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (io.ReadCloser, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			cancel()
			return nil, err
		}
		return &deadlineBody{ReadCloser: resp.Body, cancel: cancel}, nil
	})
}

// RoundBundle streams a git bundle for a given round. Returns (nil, nil) on 204 No Content.
func (c *Client) RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/bundle", url.PathEscape(name), round)
	if since != "" {
		path += "?since=" + url.QueryEscape(since)
	}
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (io.ReadCloser, error) {
		actx, cancel := context.WithTimeout(ctx, roundFileDeadline)
		resp, err := c.do(actx, server, "GET", path, nil, nil, "")
		if err != nil {
			cancel()
			return nil, err
		}
		if resp.StatusCode == http.StatusNoContent {
			_ = resp.Body.Close()
			cancel()
			return nil, nil
		}
		return &deadlineBody{ReadCloser: resp.Body, cancel: cancel}, nil
	})
}

// Ack acknowledges receipt of a round's results.
func (c *Client) Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/ack", url.PathEscape(name), round)
	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (remote.BindingView, error) {
		resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
		if err != nil {
			return remote.BindingView{}, err
		}
		defer resp.Body.Close()

		var view remote.BindingView
		if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
			return remote.BindingView{}, fmt.Errorf("decode binding view: %w", err)
		}
		return view, nil
	})
}

// Unavailable reports builder unavailability.
func (c *Client) Unavailable(ctx context.Context, server, name, token, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := "/v1/unavailable"
	if name != "" {
		path = fmt.Sprintf("/v1/bindings/%s/unavailable", url.PathEscape(name))
	}
	_, err := retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (struct{}, error) {
		reqBody, err := json.Marshal(remote.UnavailableRequest{Token: token, Reason: reason})
		if err != nil {
			return struct{}{}, fmt.Errorf("marshal unavailable request: %w", err)
		}
		sum := sha256.Sum256(reqBody)
		resp, err := c.do(ctx, server, "POST", path, reqBody, sum[:], "application/json")
		if err != nil {
			return struct{}{}, err
		}
		_ = resp.Body.Close()
		return struct{}{}, nil
	})
	return err
}

// Available lifts the rate-limit gate on subject's provider on the server's
// own (server-wide) ledger, answering with the provider it resolved and how
// many entries it removed. There is no binding-scoped counterpart: the ledger
// is server-wide.
func (c *Client) Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return retry(ctx, retryAttempts, retryBase, func(ctx context.Context) (remote.AvailableResponse, error) {
		reqBody, err := json.Marshal(remote.AvailableRequest{Subject: subject})
		if err != nil {
			return remote.AvailableResponse{}, fmt.Errorf("marshal available request: %w", err)
		}
		sum := sha256.Sum256(reqBody)
		resp, err := c.do(ctx, server, "POST", "/v1/available", reqBody, sum[:], "application/json")
		if err != nil {
			return remote.AvailableResponse{}, err
		}
		defer resp.Body.Close()

		var respBody remote.AvailableResponse
		if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
			return remote.AvailableResponse{}, fmt.Errorf("decode available: %w", err)
		}
		return respBody, nil
	})
}

// Done marks a binding as complete on the server.
func (c *Client) Done(ctx context.Context, server, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s/done", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// Unbind removes a binding from the server.
func (c *Client) Unbind(ctx context.Context, server, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s/unbind", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// Resume reactivates a binding on the server.
func (c *Client) Resume(ctx context.Context, server, name string) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s/resume", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return remote.BindingView{}, err
	}
	defer resp.Body.Close()

	var view remote.BindingView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return remote.BindingView{}, fmt.Errorf("decode binding view: %w", err)
	}
	return view, nil
}

// Stop asks the server to stop the binding's open round and returns the
// binding's view after the stop.
func (c *Client) Stop(ctx context.Context, server, name string) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s/stop", url.PathEscape(name))
	resp, err := c.do(ctx, server, "POST", path, nil, nil, "")
	if err != nil {
		return remote.BindingView{}, err
	}
	defer resp.Body.Close()

	var view remote.BindingView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return remote.BindingView{}, fmt.Errorf("decode binding view: %w", err)
	}
	return view, nil
}
