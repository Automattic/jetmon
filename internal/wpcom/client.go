package wpcom

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	NotifyModeLegacy = "legacy"
	NotifyModeModern = "modern"

	modernNotifyEndpoint  = "https://public-api.wordpress.com/wpcom/v2/jetpack-monitor/status-change"
	legacyNotifyEndpoint  = "https://jetpack.wordpress.com/jetmon/"
	defaultLegacyCertPath = "certs/jetmon.crt"
	defaultLegacyKeyPath  = "certs/jetmon.key"

	cbMaxFailures        = 5
	cbResetTimeout       = 60 * time.Second
	queueMaxSize         = 1000
	queueDropLogInterval = 10 * time.Second
)

var ErrCircuitOpen = errors.New("wpcom circuit open")

// StatusError reports an HTTP response status returned by WPCOM.
type StatusError struct {
	StatusCode int
}

func (e StatusError) Error() string {
	return fmt.Sprintf("wpcom returned %d", e.StatusCode)
}

// HTTPStatusCode extracts a WPCOM HTTP response status from err.
func HTTPStatusCode(err error) (int, bool) {
	var statusErr StatusError
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	return statusErr.StatusCode, true
}

// IsPermanentStatusError reports whether err is a per-notification WPCOM
// failure that retrying or opening the global circuit breaker cannot fix.
func IsPermanentStatusError(err error) bool {
	status, ok := HTTPStatusCode(err)
	if !ok {
		return false
	}
	return status == http.StatusNotFound || status == http.StatusGone
}

// CheckEntry represents a single check result included in a notification.
type CheckEntry struct {
	Type   int    `json:"type"` // 1=local, 2=veriflier
	Host   string `json:"host"`
	Status int    `json:"status"`
	RTT    int64  `json:"rtt"`
	Code   int    `json:"code"`
}

// Notification is the payload sent to the WPCOM API on a status change.
type Notification struct {
	BlogID           int64        `json:"blog_id"`
	MonitorURL       string       `json:"monitor_url"`
	StatusID         int          `json:"status_id"`
	LastCheck        string       `json:"last_check"`
	LastStatusChange string       `json:"last_status_change"`
	StatusType       string       `json:"status_type"`
	Checks           []CheckEntry `json:"checks"`
}

type ClientConfig struct {
	AuthToken         string
	Hostname          string
	Mode              string
	ModernEndpoint    string
	LegacyEndpoint    string
	LegacyCertPath    string
	LegacyKeyPath     string
	LegacyInsecureTLS bool
	HTTPClient        *http.Client
}

type runtimeConfig struct {
	authToken         string
	hostname          string
	mode              string
	modernEndpoint    string
	legacyEndpoint    string
	legacyCertPath    string
	legacyKeyPath     string
	legacyInsecureTLS bool
}

type legacyNotification struct {
	BlogID           int64        `json:"blog_id"`
	MonitorURL       string       `json:"monitor_url"`
	StatusID         int          `json:"status_id"`
	LastCheck        string       `json:"last_check"`
	LastStatusChange string       `json:"last_status_change"`
	Checks           []CheckEntry `json:"checks"`
	Token            string       `json:"token"`
}

// Client sends notifications to the WPCOM API with circuit breaker protection.
type Client struct {
	httpClient *http.Client

	configMu       sync.RWMutex
	config         runtimeConfig
	legacyClient   *http.Client
	legacyClientID string

	mu            sync.Mutex
	failures      int
	circuitOpen   bool
	circuitOpenAt time.Time
	queue         []queuedNotification

	lastQueueDropLog    time.Time
	queueDropSuppressed int
}

type queuedNotification struct {
	n        Notification
	queuedAt time.Time
}

// New creates a new WPCOM client.
func New(authToken, hostname string) *Client {
	return NewWithConfig(ClientConfig{
		AuthToken:         authToken,
		Hostname:          hostname,
		LegacyInsecureTLS: true,
	})
}

func NewWithConfig(cfg ClientConfig) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	c := &Client{httpClient: httpClient}
	c.Configure(cfg)
	return c
}

func (c *Client) Configure(cfg ClientConfig) {
	if c == nil {
		return
	}
	normalized := normalizeClientConfig(cfg)
	c.configMu.Lock()
	c.config = normalized
	c.legacyClient = nil
	c.legacyClientID = ""
	if cfg.HTTPClient != nil {
		c.httpClient = cfg.HTTPClient
	} else if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	c.configMu.Unlock()
}

func normalizeClientConfig(cfg ClientConfig) runtimeConfig {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = NotifyModeLegacy
	}
	modernEndpoint := strings.TrimSpace(cfg.ModernEndpoint)
	if modernEndpoint == "" {
		modernEndpoint = modernNotifyEndpoint
	}
	legacyEndpoint := strings.TrimSpace(cfg.LegacyEndpoint)
	if legacyEndpoint == "" {
		legacyEndpoint = legacyNotifyEndpoint
	}
	certPath := strings.TrimSpace(cfg.LegacyCertPath)
	if certPath == "" {
		certPath = defaultLegacyCertPath
	}
	keyPath := strings.TrimSpace(cfg.LegacyKeyPath)
	if keyPath == "" {
		keyPath = defaultLegacyKeyPath
	}
	return runtimeConfig{
		authToken:         cfg.AuthToken,
		hostname:          cfg.Hostname,
		mode:              mode,
		modernEndpoint:    modernEndpoint,
		legacyEndpoint:    legacyEndpoint,
		legacyCertPath:    certPath,
		legacyKeyPath:     keyPath,
		legacyInsecureTLS: cfg.LegacyInsecureTLS,
	}
}

func (c *Client) configSnapshot() runtimeConfig {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.config
}

// Notify sends a status change notification. If the circuit is open, the
// notification is queued and retried when the circuit closes.
// The mutex is never held during HTTP calls to avoid blocking callers.
func (c *Client) Notify(n Notification) error {
	c.mu.Lock()

	if c.circuitOpen {
		if time.Since(c.circuitOpenAt) > cbResetTimeout {
			log.Printf("wpcom: circuit breaker resetting after timeout")
			c.circuitOpen = false
			c.failures = 0
			// Drain the queue outside the lock; send the current notification normally below.
			toFlush := c.queue
			c.queue = nil
			c.mu.Unlock()
			c.sendFlush(toFlush)
		} else {
			c.enqueue(n)
			c.mu.Unlock()
			return fmt.Errorf("%w, notification queued", ErrCircuitOpen)
		}
	} else {
		c.mu.Unlock()
	}

	if err := c.send(n); err != nil {
		if IsPermanentStatusError(err) {
			return err
		}
		c.mu.Lock()
		c.failures++
		if c.failures >= cbMaxFailures && !c.circuitOpen {
			c.circuitOpen = true
			c.circuitOpenAt = time.Now()
			log.Printf("wpcom: circuit breaker opened after %d failures", c.failures)
		}
		c.mu.Unlock()
		return err
	}

	c.mu.Lock()
	c.failures = 0
	c.mu.Unlock()
	return nil
}

func (c *Client) send(n Notification) error {
	cfg := c.configSnapshot()
	switch cfg.mode {
	case NotifyModeModern:
		return c.sendModern(cfg, n)
	case NotifyModeLegacy:
		return c.sendLegacy(cfg, n)
	default:
		return fmt.Errorf("unsupported wpcom notify mode %q", cfg.mode)
	}
}

func (c *Client) sendModern(cfg runtimeConfig, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, cfg.modernEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.authToken)

	resp, err := c.baseHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("post notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return StatusError{StatusCode: resp.StatusCode}
	}
	return nil
}

func (c *Client) sendLegacy(cfg runtimeConfig, n Notification) error {
	body, err := json.Marshal(legacyNotification{
		BlogID:           n.BlogID,
		MonitorURL:       n.MonitorURL,
		StatusID:         n.StatusID,
		LastCheck:        n.LastCheck,
		LastStatusChange: n.LastStatusChange,
		Checks:           n.Checks,
		Token:            cfg.authToken,
	})
	if err != nil {
		return fmt.Errorf("marshal legacy notification: %w", err)
	}
	endpoint, err := legacyNotifyURL(cfg.legacyEndpoint, string(body))
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build legacy request: %w", err)
	}

	client, err := c.legacyHTTPClient(cfg)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("legacy get notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return StatusError{StatusCode: resp.StatusCode}
	}
	replyBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read legacy notification response: %w", err)
	}
	if strings.TrimSpace(string(replyBody)) == "" {
		return errors.New("legacy wpcom notification response was empty")
	}
	var reply struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(replyBody, &reply); err != nil {
		return fmt.Errorf("parse legacy notification response: %w", err)
	}
	if !reply.Success {
		return fmt.Errorf("legacy wpcom notification response success=false data=%s", strings.TrimSpace(string(reply.Data)))
	}
	return nil
}

func legacyNotifyURL(endpoint, encodedData string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse legacy endpoint: %w", err)
	}
	q := u.Query()
	q.Set("data", encodedData)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) legacyHTTPClient(cfg runtimeConfig) (*http.Client, error) {
	u, err := url.Parse(cfg.legacyEndpoint)
	if err != nil {
		return nil, fmt.Errorf("parse legacy endpoint: %w", err)
	}
	if u.Scheme != "https" {
		return c.baseHTTPClient(), nil
	}

	clientID := strings.Join([]string{
		cfg.legacyCertPath,
		cfg.legacyKeyPath,
		fmt.Sprint(cfg.legacyInsecureTLS),
	}, "\x00")

	c.configMu.RLock()
	if c.legacyClient != nil && c.legacyClientID == clientID {
		client := c.legacyClient
		c.configMu.RUnlock()
		return client, nil
	}
	c.configMu.RUnlock()

	cert, err := tls.LoadX509KeyPair(cfg.legacyCertPath, cfg.legacyKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load legacy wpcom client certificate: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: cfg.legacyInsecureTLS, // Preserve v1 production TLS behavior for this compatibility mode.
	}
	client := &http.Client{
		Timeout:   c.baseHTTPClient().Timeout,
		Transport: transport,
	}

	c.configMu.Lock()
	c.legacyClient = client
	c.legacyClientID = clientID
	c.configMu.Unlock()
	return client, nil
}

func (c *Client) baseHTTPClient() *http.Client {
	c.configMu.RLock()
	client := c.httpClient
	c.configMu.RUnlock()
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) enqueue(n Notification) {
	if len(c.queue) >= queueMaxSize {
		c.logQueueDrop(c.queue[0].n.BlogID)
		c.queue = c.queue[1:]
	}
	c.queue = append(c.queue, queuedNotification{n: n, queuedAt: time.Now()})
}

func (c *Client) logQueueDrop(blogID int64) {
	now := time.Now()
	if c.lastQueueDropLog.IsZero() || now.Sub(c.lastQueueDropLog) >= queueDropLogInterval {
		if c.queueDropSuppressed > 0 {
			log.Printf(
				"wpcom: queue full, dropping oldest notification for blog_id=%d (suppressed %d similar drops)",
				blogID,
				c.queueDropSuppressed,
			)
		} else {
			log.Printf("wpcom: queue full, dropping oldest notification for blog_id=%d", blogID)
		}
		c.lastQueueDropLog = now
		c.queueDropSuppressed = 0
		return
	}
	c.queueDropSuppressed++
}

// sendFlush sends previously queued notifications without holding the mutex.
func (c *Client) sendFlush(queue []queuedNotification) {
	if len(queue) == 0 {
		return
	}
	log.Printf("wpcom: flushing %d queued notifications", len(queue))
	for _, q := range queue {
		if err := c.send(q.n); err != nil {
			log.Printf("wpcom: flush failed for blog_id=%d: %v", q.n.BlogID, err)
		}
	}
}

// IsCircuitOpen reports whether the circuit breaker is currently open.
func (c *Client) IsCircuitOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.circuitOpen
}

// QueueDepth returns the number of queued notifications.
func (c *Client) QueueDepth() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}
