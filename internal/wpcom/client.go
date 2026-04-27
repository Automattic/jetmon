package wpcom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	notifyEndpoint = "https://public-api.wordpress.com/wpcom/v2/jetpack-monitor/status-change"

	cbMaxFailures  = 5
	cbResetTimeout = 60 * time.Second
	queueMaxSize   = 1000
)

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

// Client sends notifications to the WPCOM API with circuit breaker protection.
type Client struct {
	authToken  string
	notifyURL  string // overrides notifyEndpoint when set (used in tests)
	httpClient *http.Client
	hostname   string

	mu            sync.Mutex
	failures      int
	circuitOpen   bool
	circuitOpenAt time.Time
	queue         []queuedNotification
}

type queuedNotification struct {
	n        Notification
	queuedAt time.Time
}

// New creates a new WPCOM client.
func New(authToken, hostname string) *Client {
	return &Client{
		authToken: authToken,
		hostname:  hostname,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
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
			return fmt.Errorf("wpcom circuit open, notification queued")
		}
	} else {
		c.mu.Unlock()
	}

	if err := c.send(n); err != nil {
		c.mu.Lock()
		c.failures++
		if c.failures >= cbMaxFailures {
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
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	url := c.notifyURL
	if url == "" {
		url = notifyEndpoint
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("wpcom returned %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) enqueue(n Notification) {
	if len(c.queue) >= queueMaxSize {
		log.Printf("wpcom: queue full, dropping oldest notification for blog_id=%d", c.queue[0].n.BlogID)
		c.queue = c.queue[1:]
	}
	c.queue = append(c.queue, queuedNotification{n: n, queuedAt: time.Now()})
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
