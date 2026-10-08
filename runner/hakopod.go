package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	config "github.com/SyneHQ/apollo"
)

const maxInvocationResponse = 512 << 10
const maxInvocationLogs = 64 << 10

var invocationName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var invocationImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_-]*@sha256:[a-f0-9]{64}$`)
var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type ownerScopeKey struct{}

// WithOwnerScope carries the owner already verified by the calling service.
func WithOwnerScope(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerScopeKey{}, owner)
}

type HakopodRunner struct {
	config       config.HakopodConfig
	client       *http.Client
	key          string
	pollInterval time.Duration
}

type invocationRecord struct {
	ID             string `json:"id"`
	ApplicationID  string `json:"application_id"`
	Service        string `json:"service"`
	Revision       int64  `json:"revision"`
	Image          string `json:"image"`
	CorrelationID  string `json:"correlation_id"`
	Status         string `json:"status"`
	CleanupPending bool   `json:"cleanup_pending"`
}

type invocationCreate struct {
	ExpectedRevision int64             `json:"expected_revision"`
	ExpectedImage    string            `json:"expected_image"`
	CorrelationID    string            `json:"correlation_id"`
	OwnerScope       string            `json:"owner_scope"`
	Inputs           map[string]string `json:"inputs"`
}

type invocationHTTPError struct{ status int }

func (e *invocationHTTPError) Error() string {
	return fmt.Sprintf("Hakopod invocation API returned HTTP %d", e.status)
}

func NewHakopodRunner(cfg config.HakopodConfig, key, caPEM string) (*HakopodRunner, error) {
	u, err := url.Parse(cfg.APIURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Hakopod API URL must be an HTTPS origin without credentials or query parameters")
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(cfg.ApplicationID) || cfg.Revision < 1 || len(cfg.Templates) < 1 || len(cfg.Templates) > 16 {
		return nil, errors.New("Hakopod runner requires an application ID, positive revision and 1 to 16 reviewed templates")
	}
	if len(key) < 32 || len(key) > 512 || strings.ContainsAny(key, "\r\n\t ") {
		return nil, errors.New("APOLLO_HAKOPOD_API_KEY must contain a scoped API credential")
	}
	templates := make(map[string]config.HakopodTemplate, len(cfg.Templates))
	services := make(map[string]bool)
	for command, t := range cfg.Templates {
		if t.RequestImage == "" {
			t.RequestImage = t.Image
		}
		if len(t.RequestImage) > 512 || strings.ContainsAny(t.RequestImage, " \r\n\t") {
			return nil, errors.New("Hakopod request image alias is invalid")
		}
		templates[command] = t
		if services[t.Service] {
			return nil, errors.New("each Hakopod command must use a separate reviewed service")
		}
		services[t.Service] = true
		if !invocationName.MatchString(command) || !invocationName.MatchString(t.Service) || !invocationImage.MatchString(t.Image) || t.Prefix == "" || t.CPU == "" || t.Memory == "" {
			return nil, errors.New("Hakopod templates require a command, service, immutable image, prefix and fixed resources")
		}
	}
	cfg.Templates = templates
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caPEM != "" && !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("APOLLO_HAKOPOD_CA_PEM must contain valid CA certificates")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 4, IdleConnTimeout: 60 * time.Second}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	return &HakopodRunner{config: cfg, key: key, pollInterval: time.Second, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Hakopod API redirects are not permitted")
	}}}, nil
}

func (r *HakopodRunner) collection(service string) string {
	return r.config.APIURL + "/api/v1/applications/" + url.PathEscape(r.config.ApplicationID) + "/services/" + url.PathEscape(service) + "/invocations"
}
func (r *HakopodRunner) request(ctx context.Context, method, endpoint, owner, key string, body any, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode Hakopod invocation request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return errors.New("cannot construct Hakopod invocation request")
	}
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("X-Hakopod-Owner-Scope", owner)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := r.client.Do(req)
	if err != nil {
		return errors.New("Hakopod invocation transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return &invocationHTTPError{status: res.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxInvocationResponse+1))
	if err != nil || len(raw) > maxInvocationResponse {
		return errors.New("Hakopod invocation response exceeds its limit or cannot be read")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("Hakopod invocation API returned invalid JSON")
	}
	return nil
}

func (r *HakopodRunner) validateRecord(v invocationRecord, t config.HakopodTemplate, correlation string) error {
	if !invocationName.MatchString(v.ID) || v.ApplicationID != r.config.ApplicationID || v.Service != t.Service || v.Revision != r.config.Revision || v.Image != t.Image || v.CorrelationID != correlation {
		return errors.New("Hakopod invocation receipt does not match the reviewed template")
	}
	switch v.Status {
	case "queued", "starting", "running", "succeeded", "failed", "cancelled":
		return nil
	default:
		return errors.New("Hakopod invocation status is invalid")
	}
}

func (r *HakopodRunner) lookup(ctx context.Context, t config.HakopodTemplate, owner, name, key string, active bool) ([]invocationRecord, error) {
	query := url.Values{"correlation_id": {name}}
	if active {
		query.Set("active", "true")
	}
	var result struct {
		Items []invocationRecord `json:"items"`
	}
	if err := r.request(ctx, http.MethodGet, r.collection(t.Service)+"?"+query.Encode(), owner, key, nil, &result); err != nil {
		return nil, err
	}
	limit := 4
	if key != "" {
		limit = 1
	}
	if len(result.Items) > limit {
		return nil, errors.New("Hakopod invocation lookup exceeds its limit")
	}
	for _, item := range result.Items {
		if err := r.validateRecord(item, t, name); err != nil {
			return nil, err
		}
	}
	return result.Items, nil
}

func (r *HakopodRunner) cancel(ctx context.Context, t config.HakopodTemplate, owner, name, id string) error {
	var result invocationRecord
	if err := r.request(ctx, http.MethodPost, r.collection(t.Service)+"/"+url.PathEscape(id)+"/cancel", owner, "", struct{}{}, &result); err != nil {
		return err
	}
	if result.ID != id {
		return errors.New("Hakopod cancellation receipt has a different invocation ID")
	}
	return r.validateRecord(result, t, name)
}

// recoverCancellation does not create or replay jobs when submission acknowledgement is lost.
func (r *HakopodRunner) recoverCancellation(t config.HakopodTemplate, owner, name, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := r.lookup(ctx, t, owner, name, key, false)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("Hakopod submission outcome is unknown")
	}
	for _, item := range items {
		if err := r.cancel(ctx, t, owner, name, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func withinTemplateResources(r Resources, t config.HakopodTemplate) bool {
	cpu, e1 := parseNanoCPUs(r.CPU)
	memory, e2 := parseMemoryBytes(r.Memory)
	maxCPU, e3 := parseNanoCPUs(t.CPU)
	maxMemory, e4 := parseMemoryBytes(t.Memory)
	return e1 == nil && e2 == nil && e3 == nil && e4 == nil && cpu > 0 && memory > 0 && cpu <= maxCPU && memory <= maxMemory
}

func invocationKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func validInvocationPayload(value string) bool {
	if len(value) > 90000 {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) > 65536 || !utf8.Valid(raw) {
		return false
	}
	var data map[string]json.RawMessage
	return json.Unmarshal(raw, &data) == nil && data != nil
}

func (r *HakopodRunner) RunJob(ctx context.Context, prefix string, req JobRequest) (output string, resultErr error) {
	t, ok := r.config.Templates[req.Command]
	if !ok || !ownerPattern.MatchString(req.OwnerScope) || !invocationName.MatchString(req.Name) || req.JobID == "" || len(req.JobID) > 256 || strings.ContainsAny(req.JobID, "\r\n") || req.Image != t.RequestImage || prefix != t.Prefix || !withinTemplateResources(req.Resources, t) || req.Overrides != nil || !validInvocationPayload(req.ArgsJSONBase64) {
		return "", errors.New("job does not match an authorized Hakopod template")
	}
	ctx, stop := context.WithTimeout(ctx, 20*time.Minute)
	defer stop()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := invocationKey(req.JobID)
	body := invocationCreate{ExpectedRevision: r.config.Revision, ExpectedImage: t.Image, CorrelationID: req.Name, OwnerScope: req.OwnerScope, Inputs: map[string]string{"payload": req.ArgsJSONBase64}}
	var current invocationRecord
	err := r.request(ctx, http.MethodPost, r.collection(t.Service), req.OwnerScope, key, body, &current)
	if err != nil {
		var httpErr *invocationHTTPError
		if !errors.As(err, &httpErr) || httpErr.status >= 500 {
			if cancelErr := r.recoverCancellation(t, req.OwnerScope, req.Name, key); cancelErr != nil {
				return "", errors.New("Hakopod submission failed and cancellation could not be confirmed")
			}
		}
		return "", err
	}
	if err = r.validateRecord(current, t, req.Name); err != nil {
		return "", err
	}
	id := current.ID
	completed := false
	defer func() {
		if !completed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.cancel(cleanup, t, req.OwnerScope, req.Name, id); err != nil {
				resultErr = errors.New("job result is unavailable and Hakopod cancellation could not be confirmed")
			}
		}
	}()
	for {
		if !current.CleanupPending {
			switch current.Status {
			case "succeeded", "failed", "cancelled":
				completed = true
				var logs struct {
					Text      string `json:"text"`
					Truncated bool   `json:"truncated"`
				}
				if err := r.request(ctx, http.MethodGet, r.collection(t.Service)+"/"+url.PathEscape(id)+"/logs", req.OwnerScope, "", nil, &logs); err != nil {
					return "", err
				}
				if len(logs.Text) > maxInvocationLogs {
					return "", errors.New("Hakopod invocation logs exceed their limit")
				}
				if logs.Truncated {
					logs.Text += "\n[worker logs truncated]"
				}
				if current.Status != "succeeded" {
					return logs.Text, fmt.Errorf("Hakopod invocation %s", current.Status)
				}
				return logs.Text, nil
			}
		}
		timer := time.NewTimer(r.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		if err := r.request(ctx, http.MethodGet, r.collection(t.Service)+"/"+url.PathEscape(id), req.OwnerScope, "", nil, &current); err != nil {
			return "", err
		}
		if current.ID != id {
			return "", errors.New("Hakopod status receipt has a different invocation ID")
		}
		if err := r.validateRecord(current, t, req.Name); err != nil {
			return "", err
		}
	}
}

func (r *HakopodRunner) DeleteJob(ctx context.Context, name string) error {
	owner, _ := ctx.Value(ownerScopeKey{}).(string)
	if !ownerPattern.MatchString(owner) || !invocationName.MatchString(name) {
		return errors.New("Hakopod cancellation requires a verified owner and job name")
	}
	for _, t := range r.config.Templates {
		items, err := r.lookup(ctx, t, owner, name, "", true)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := r.cancel(ctx, t, owner, name, item.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *HakopodRunner) UpdateSchedule(context.Context, string, string) error {
	return errors.New("Hakopod schedules must be stored and executed by Apollo")
}
