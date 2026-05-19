package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/config"
)

const dbReloadPingTimeout = 10 * time.Second

type poolSettings struct {
	maxOpen     int
	maxIdle     int
	maxLifetime time.Duration
}

type Manager struct {
	readDB  *sql.DB
	writeDB *sql.DB

	readConnector  *roleConnector
	writeConnector *roleConnector

	settings poolSettings

	mu        sync.RWMutex
	selection endpointSelection
}

type roleConnector struct {
	role    string
	current atomic.Value // *connectorSnapshot
	next    atomic.Uint64
}

type connectorSnapshot struct {
	endpoints []driver.Connector
}

type snapshotConnector struct {
	role string
	snap *connectorSnapshot
	next atomic.Uint64
}

func NewManager(dbCfg *config.DBConfig, appCfg *config.Config) (*Manager, error) {
	sel, err := loadEndpointSelection(dbCfg)
	if err != nil {
		return nil, err
	}
	settings := dbPoolSettings(appCfg)
	readConnector, err := newRoleConnector("read", sel.Read)
	if err != nil {
		return nil, err
	}
	writeConnector, err := newRoleConnector("write", sel.Write)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		readConnector:  readConnector,
		writeConnector: writeConnector,
		settings:       settings,
		selection:      sel,
	}
	m.readDB = sql.OpenDB(readConnector)
	m.writeDB = sql.OpenDB(writeConnector)
	applyPoolSettings(m.readDB, settings)
	applyPoolSettings(m.writeDB, settings)
	return m, nil
}

func dbPoolSettings(cfg *config.Config) poolSettings {
	maxOpenConns := maxOpenConnectionsForConfig(cfg, runtime.GOMAXPROCS(0))
	return poolSettings{
		maxOpen:     maxOpenConns,
		maxIdle:     maxOpenConns / 2,
		maxLifetime: 5 * time.Minute,
	}
}

func applyPoolSettings(conn *sql.DB, settings poolSettings) {
	conn.SetMaxOpenConns(settings.maxOpen)
	conn.SetMaxIdleConns(settings.maxIdle)
	conn.SetConnMaxLifetime(settings.maxLifetime)
}

func newRoleConnector(role string, endpoints []endpointConfig) (*roleConnector, error) {
	snap, err := buildConnectorSnapshot(endpoints)
	if err != nil {
		return nil, err
	}
	c := &roleConnector{role: role}
	c.current.Store(snap)
	return c, nil
}

func buildConnectorSnapshot(endpoints []endpointConfig) (*connectorSnapshot, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("no database endpoints configured")
	}
	snap := &connectorSnapshot{
		endpoints: make([]driver.Connector, 0, len(endpoints)),
	}
	for _, ep := range endpoints {
		connector, err := mysql.NewConnector(ep.mysql.Clone())
		if err != nil {
			return nil, fmt.Errorf("mysql connector for %s: %w", ep.Label, err)
		}
		snap.endpoints = append(snap.endpoints, connector)
	}
	return snap, nil
}

func (c *roleConnector) Connect(ctx context.Context) (driver.Conn, error) {
	v := c.current.Load()
	if v == nil {
		return nil, fmt.Errorf("%s db connector is not configured", c.role)
	}
	return connectViaSnapshot(ctx, c.role, v.(*connectorSnapshot), &c.next)
}

func (c *roleConnector) Driver() driver.Driver {
	return mysql.MySQLDriver{}
}

func (c *roleConnector) update(snap *connectorSnapshot) {
	c.current.Store(snap)
}

func (c *snapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return connectViaSnapshot(ctx, c.role, c.snap, &c.next)
}

func (c *snapshotConnector) Driver() driver.Driver {
	return mysql.MySQLDriver{}
}

func connectViaSnapshot(ctx context.Context, role string, snap *connectorSnapshot, next *atomic.Uint64) (driver.Conn, error) {
	if snap == nil || len(snap.endpoints) == 0 {
		return nil, fmt.Errorf("%s db connector has no endpoints", role)
	}
	start := int(next.Add(1)-1) % len(snap.endpoints)
	var lastErr error
	for i := 0; i < len(snap.endpoints); i++ {
		connector := snap.endpoints[(start+i)%len(snap.endpoints)]
		conn, err := connector.Connect(ctx)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%s db connect failed across %d endpoint(s): %w", role, len(snap.endpoints), lastErr)
}

func (m *Manager) ReadDB() *sql.DB {
	if m == nil {
		return nil
	}
	return m.readDB
}

func (m *Manager) WriteDB() *sql.DB {
	if m == nil {
		return nil
	}
	return m.writeDB
}

func (m *Manager) Ping(ctx context.Context) error {
	if m == nil {
		return errors.New("database manager is not initialized")
	}
	if err := m.writeDB.PingContext(ctx); err != nil {
		return fmt.Errorf("write db ping: %w", err)
	}
	if err := m.readDB.PingContext(ctx); err != nil {
		return fmt.Errorf("read db ping: %w", err)
	}
	return nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.readDB != nil {
		err = errors.Join(err, m.readDB.Close())
	}
	if m.writeDB != nil && m.writeDB != m.readDB {
		err = errors.Join(err, m.writeDB.Close())
	}
	return err
}

func (m *Manager) Summary() string {
	if m == nil {
		return "uninitialized"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	readLabels := endpointLabels(m.selection.Read)
	writeLabels := endpointLabels(m.selection.Write)
	return fmt.Sprintf("source=%s read=%s write=%s signature=%s",
		m.selection.Source,
		strings.Join(readLabels, ","),
		strings.Join(writeLabels, ","),
		redactedSelectionFingerprint(m.selection.Signature),
	)
}

func endpointLabels(endpoints []endpointConfig) []string {
	labels := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		labels = append(labels, ep.Label)
	}
	return labels
}

func redactedSelectionFingerprint(signature string) string {
	sum := sha256.Sum256([]byte(signature))
	return fmt.Sprintf("%x", sum[:6])
}

func (m *Manager) Reload(ctx context.Context) (bool, error) {
	if m == nil {
		return false, errors.New("database manager is not initialized")
	}
	m.mu.RLock()
	current := m.selection
	m.mu.RUnlock()
	if !strings.HasPrefix(current.Source, "server-map:") {
		return false, nil
	}

	dbCfg := config.GetDB()
	next, err := loadEndpointSelection(dbCfg)
	if err != nil {
		return false, err
	}
	if next.Signature == current.Signature {
		return false, nil
	}

	readSnap, err := buildConnectorSnapshot(next.Read)
	if err != nil {
		return false, err
	}
	writeSnap, err := buildConnectorSnapshot(next.Write)
	if err != nil {
		return false, err
	}
	if err := pingSnapshot(ctx, "write", writeSnap); err != nil {
		return false, err
	}
	if err := pingSnapshot(ctx, "read", readSnap); err != nil {
		return false, err
	}

	m.writeConnector.update(writeSnap)
	m.readConnector.update(readSnap)
	m.flushIdleConnections()

	m.mu.Lock()
	m.selection = next
	m.mu.Unlock()
	return true, nil
}

func pingSnapshot(ctx context.Context, role string, snap *connectorSnapshot) error {
	pingCtx, cancel := context.WithTimeout(ctx, dbReloadPingTimeout)
	defer cancel()
	conn := sql.OpenDB(&snapshotConnector{role: role, snap: snap})
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(0)
	conn.SetConnMaxLifetime(time.Minute)
	defer conn.Close()
	if err := conn.PingContext(pingCtx); err != nil {
		return fmt.Errorf("%s db ping after config reload: %w", role, err)
	}
	return nil
}

func (m *Manager) flushIdleConnections() {
	for _, conn := range []*sql.DB{m.readDB, m.writeDB} {
		if conn == nil {
			continue
		}
		conn.SetMaxIdleConns(0)
		applyPoolSettings(conn, m.settings)
	}
}

func StartConfigReloader(ctx context.Context, interval time.Duration) {
	m := manager
	if m == nil {
		return
	}
	m.mu.RLock()
	source := m.selection.Source
	m.mu.RUnlock()
	if !strings.HasPrefix(source, "server-map:") {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		initial := stableReloadJitter(interval)
		timer := time.NewTimer(initial)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			reloadCtx, cancel := context.WithTimeout(ctx, dbReloadPingTimeout*2)
			changed, err := m.Reload(reloadCtx)
			cancel()
			if err != nil {
				log.Printf("db config reload failed; keeping existing pools: %v", err)
			} else if changed {
				log.Printf("db config reloaded: %s", m.Summary())
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func ReloadConfig(ctx context.Context) (bool, error) {
	if manager == nil {
		return false, errors.New("database manager is not initialized")
	}
	return manager.Reload(ctx)
}

func Summary() string {
	if manager == nil {
		return "uninitialized"
	}
	return manager.Summary()
}

func stableReloadJitter(interval time.Duration) time.Duration {
	maxJitter := min(interval/5, 2*time.Minute)
	if maxJitter <= 0 {
		return 0
	}
	seed := sha256.Sum256([]byte(Hostname()))
	n := int64(seed[0])<<24 | int64(seed[1])<<16 | int64(seed[2])<<8 | int64(seed[3])
	if n < 0 {
		n = -n
	}
	jitter := time.Duration(n % int64(maxJitter+1))
	if jitter == 0 {
		jitter = maxJitter / 2
	}
	return jitter
}
