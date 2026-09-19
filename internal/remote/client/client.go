package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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

	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/serve"
)

var (
	ErrUnreachable   = errors.New("remote server unreachable")
	ErrCertChanged   = errors.New("pinned fingerprint mismatch")
	ErrUnknownServer = errors.New("unknown server")
	ErrVersion       = errors.New("server rejected protocol version (426)")
)

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

// Client communicates with remote relay servers using ed25519 request signing.
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
				fp := serve.FingerprintOf(rawCerts[0])
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

// WhoAmI fetches caller identification from the server.
func (c *Client) WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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
}

// Candidates lists available builder candidates on the server.
func (c *Client) Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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
}

// StartRound begins a round on the server, spooling the multipart form (plan + bundle).
func (c *Client) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tmp, err := os.CreateTemp("", "relay-start-round-*.tmp")
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
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return remote.BindingView{}, fmt.Errorf("seek start: %w", err)
	}

	bodySHA := hasher.Sum(nil)
	path := fmt.Sprintf("/v1/bindings/%s/rounds", url.PathEscape(name))

	resp, err := c.doRequest(ctx, server, "POST", path, tmp, size, bodySHA, mw.FormDataContentType())
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

// RoundFile streams a file (report, diff, log) for a given round.
func (c *Client) RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/files/%s", url.PathEscape(name), round, url.PathEscape(kind))
	resp, err := c.do(ctx, server, "GET", path, nil, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// RoundBundle streams a git bundle for a given round. Returns (nil, nil) on 204 No Content.
func (c *Client) RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/bundle", url.PathEscape(name), round)
	if since != "" {
		path += "?since=" + url.QueryEscape(since)
	}
	resp, err := c.do(ctx, server, "GET", path, nil, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		_ = resp.Body.Close()
		return nil, nil
	}
	return resp.Body, nil
}

// Ack acknowledges receipt of a round's results.
func (c *Client) Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/v1/bindings/%s/rounds/%d/ack", url.PathEscape(name), round)
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

// Unavailable reports builder unavailability.
func (c *Client) Unavailable(ctx context.Context, server, name, token, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	path := "/v1/unavailable"
	if name != "" {
		path = fmt.Sprintf("/v1/bindings/%s/unavailable", url.PathEscape(name))
	}
	reqBody, err := json.Marshal(remote.UnavailableRequest{Token: token, Reason: reason})
	if err != nil {
		return fmt.Errorf("marshal unavailable request: %w", err)
	}
	sum := sha256.Sum256(reqBody)
	resp, err := c.do(ctx, server, "POST", path, reqBody, sum[:], "application/json")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
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
