// Package controllerclient calls the API of a worker machine controller,
// with the host's bearer token, over TLS with the host's CA when the URL is https.
package controllerclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anovimous/automata-worker-monitor/internal/controllerapi"
	"github.com/anovimous/automata-worker-monitor/internal/controlplane/config"
)

// an answer of the controller is at most this big (a snapshot of 50 workers is a few hundred KiB)
const maxAnswer = 16 << 20

// APIError is an error answer of the controller.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("worker machine controller answered %d %s: %s", e.Status, e.Code, e.Message)
}

// IsNotFound tells whether err is a 404 of the controller.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

type Client struct {
	host  string
	base  *url.URL
	token string
	http  *http.Client
}

// New makes the client of a host. The calls have no timeout of their own: each caller gives its context one (a stop
// waits up to 600 s, a log stream as long as it is read).
func New(h config.Host) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(h.WorkerMachineControllerURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("host %s: bad URL: %w", h.Name, err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if h.CAFile != "" {
		pem, err := os.ReadFile(h.CAFile)
		if err != nil {
			return nil, fmt.Errorf("host %s: failed to read the CA file: %w", h.Name, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("host %s: the CA file holds no certificate", h.Name)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{host: h.Name, base: base, token: h.ControlToken, http: &http.Client{Transport: transport}}, nil
}

// Host is the name of the host of this client.
func (c *Client) Host() string {
	return c.host
}

// do sends a request and returns the answer; an answer that is not 2xx is an *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("host %s: %s %s: %w", c.host, method, path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	var er controllerapi.ErrorResponse
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &er) != nil || er.Error.Code == "" {
		return nil, &APIError{Status: resp.StatusCode, Code: "UNKNOWN", Message: strings.TrimSpace(string(raw))}
	}
	return nil, &APIError{Status: resp.StatusCode, Code: er.Error.Code, Message: er.Error.Message}
}

// call sends a request and decodes the JSON answer into out (nil to drop it).
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.do(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(out); err != nil {
		return fmt.Errorf("host %s: %s %s: bad answer: %w", c.host, method, path, err)
	}
	return nil
}

func workerPath(id string, rest string) string {
	return "/v1/workers/" + url.PathEscape(id) + rest
}

func (c *Client) Health(ctx context.Context) (controllerapi.HealthResponse, error) {
	var h controllerapi.HealthResponse
	return h, c.call(ctx, http.MethodGet, "/v1/health", nil, nil, &h)
}

func (c *Client) Snapshot(ctx context.Context) (controllerapi.Snapshot, error) {
	var s controllerapi.Snapshot
	return s, c.call(ctx, http.MethodGet, "/v1/snapshot", nil, nil, &s)
}

func (c *Client) Worker(ctx context.Context, id string) (controllerapi.WorkerDetail, error) {
	var d controllerapi.WorkerDetail
	return d, c.call(ctx, http.MethodGet, workerPath(id, ""), nil, nil, &d)
}

func (c *Client) Create(ctx context.Context, req controllerapi.CreateWorkerRequest) (controllerapi.CreateWorkerResponse, error) {
	var r controllerapi.CreateWorkerResponse
	return r, c.call(ctx, http.MethodPost, "/v1/workers", nil, req, &r)
}

func (c *Client) Start(ctx context.Context, id string) (controllerapi.WorkerObservation, error) {
	var o controllerapi.WorkerObservation
	return o, c.call(ctx, http.MethodPost, workerPath(id, "/start"), nil, nil, &o)
}

// Signal sends SIGUSR1 (drain) or SIGUSR2 (undrain).
func (c *Client) Signal(ctx context.Context, id, signal string) (controllerapi.WorkerObservation, error) {
	var o controllerapi.WorkerObservation
	return o, c.call(ctx, http.MethodPost, workerPath(id, "/signal"), nil, controllerapi.SignalRequest{Signal: signal}, &o)
}

// Stop returns when the container stopped, after at most timeout plus Docker's kill.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) (controllerapi.WorkerObservation, error) {
	var o controllerapi.WorkerObservation
	return o, c.call(ctx, http.MethodPost, workerPath(id, "/stop"), nil, controllerapi.StopRequest{TimeoutSeconds: int(timeout.Seconds())}, &o)
}

func (c *Client) Kill(ctx context.Context, id string) (controllerapi.WorkerObservation, error) {
	var o controllerapi.WorkerObservation
	return o, c.call(ctx, http.MethodPost, workerPath(id, "/kill"), nil, nil, &o)
}

func (c *Client) Restart(ctx context.Context, id string, timeout time.Duration) (controllerapi.WorkerObservation, error) {
	var o controllerapi.WorkerObservation
	return o, c.call(ctx, http.MethodPost, workerPath(id, "/restart"), nil, controllerapi.StopRequest{TimeoutSeconds: int(timeout.Seconds())}, &o)
}

func (c *Client) Remove(ctx context.Context, id string, removeVolumes bool) (controllerapi.RemoveResponse, error) {
	var r controllerapi.RemoveResponse
	q := url.Values{}
	if removeVolumes {
		q.Set("removeVolumes", "true")
	}
	return r, c.call(ctx, http.MethodDelete, workerPath(id, ""), q, nil, &r)
}

// LogOptions are the query of the logs route.
type LogOptions struct {
	Tail   int
	Since  string
	Follow bool
}

// Logs opens the log stream of a worker: an event stream with Follow, a JSON array without. The caller closes it.
func (c *Client) Logs(ctx context.Context, id string, opts LogOptions) (body io.ReadCloser, contentType string, err error) {
	q := url.Values{}
	if opts.Tail > 0 {
		q.Set("tail", strconv.Itoa(opts.Tail))
	}
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Follow {
		q.Set("follow", "true")
	}
	resp, err := c.do(ctx, http.MethodGet, workerPath(id, "/logs"), q, nil)
	if err != nil {
		return nil, "", err
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

func (c *Client) EnvFiles(ctx context.Context) ([]string, error) {
	var names []string
	return names, c.call(ctx, http.MethodGet, "/v1/env-files", nil, nil, &names)
}

func (c *Client) Images(ctx context.Context, reference string) ([]controllerapi.Image, error) {
	var imgs []controllerapi.Image
	q := url.Values{}
	if reference != "" {
		q.Set("reference", reference)
	}
	return imgs, c.call(ctx, http.MethodGet, "/v1/images", q, nil, &imgs)
}
