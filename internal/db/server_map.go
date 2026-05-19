package db

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/config"
)

const (
	serverMapDatacenter = 0
	serverMapRead       = 1
	serverMapWrite      = 2
	serverMapInternet   = 3
	serverMapInternal   = 4
	serverMapDBName     = 5
	serverMapUser       = 6
	serverMapPassword   = 7
	serverMapFieldCount = 11
)

var serverMapDatasetPattern = regexp.MustCompile(`^\s*'([^']+)'\s*=>\s*array\s*\(`)

type endpointConfig struct {
	Label     string
	Host      string
	Port      string
	Database  string
	User      string
	Password  string
	mysql     *mysql.Config
	signature string
}

type endpointSelection struct {
	Source    string
	Read      []endpointConfig
	Write     []endpointConfig
	Signature string
}

type serverMapOptions struct {
	Path       string
	Dataset    string
	Datacenter string
	Address    string
}

type serverMapRow struct {
	Datacenter string
	ReadOrder  int
	Write      bool
	Internet   string
	Internal   string
	Database   string
	User       string
	Password   string
	Index      int
	Local      bool
}

func loadEndpointSelection(cfg *config.DBConfig) (endpointSelection, error) {
	if cfg == nil {
		cfg = config.LoadDB()
	}
	if strings.TrimSpace(cfg.ServerMapPath) != "" {
		opts := serverMapOptions{
			Path:       cfg.ServerMapPath,
			Dataset:    cfg.ServerMapDataset,
			Datacenter: cfg.ServerMapDatacenter,
			Address:    cfg.ServerMapAddress,
		}
		return loadServerMapSelection(opts)
	}

	ep, err := endpointFromParts("env", cfg.Host, cfg.Port, cfg.Name, cfg.User, cfg.Password)
	if err != nil {
		return endpointSelection{}, err
	}
	return finalizeEndpointSelection(endpointSelection{
		Source: "env:DB_HOST",
		Read:   []endpointConfig{ep},
		Write:  []endpointConfig{ep},
	}), nil
}

func loadServerMapSelection(opts serverMapOptions) (endpointSelection, error) {
	opts.Path = strings.TrimSpace(opts.Path)
	if opts.Path == "" {
		return endpointSelection{}, fmt.Errorf("server map path is required")
	}
	raw, err := os.ReadFile(opts.Path)
	if err != nil {
		return endpointSelection{}, fmt.Errorf("read db server map: %w", err)
	}
	return parseServerMapSelection(raw, opts)
}

func parseServerMapSelection(raw []byte, opts serverMapOptions) (endpointSelection, error) {
	dataset := strings.TrimSpace(opts.Dataset)
	if dataset == "" {
		dataset = "misc"
	}
	address := strings.ToLower(strings.TrimSpace(opts.Address))
	if address == "" {
		address = "internet"
	}
	if address != "internet" && address != "internal" {
		return endpointSelection{}, fmt.Errorf("DB_SERVER_MAP_ADDRESS must be internet or internal")
	}
	datacenter := strings.ToLower(strings.TrimSpace(opts.Datacenter))
	if datacenter == "" {
		datacenter = inferDatacenterFromHostname(Hostname())
	}

	rows, err := parseServerMapRows(raw, dataset, datacenter)
	if err != nil {
		return endpointSelection{}, err
	}
	if len(rows) == 0 {
		return endpointSelection{}, fmt.Errorf("db server map dataset %q had no usable rows", dataset)
	}

	var writes []serverMapRow
	var reads []serverMapRow
	for _, row := range rows {
		if row.Write {
			writes = append(writes, row)
		}
		if row.ReadOrder > 0 && !isBackupDatacenter(row.Datacenter) {
			reads = append(reads, row)
		}
	}
	if len(writes) == 0 {
		return endpointSelection{}, fmt.Errorf("db server map dataset %q had no write master row", dataset)
	}
	if len(reads) == 0 {
		reads = writes
	}

	sort.SliceStable(reads, func(i, j int) bool {
		if reads[i].Local != reads[j].Local {
			return reads[i].Local
		}
		if reads[i].ReadOrder != reads[j].ReadOrder {
			return reads[i].ReadOrder < reads[j].ReadOrder
		}
		return reads[i].Index < reads[j].Index
	})
	sort.SliceStable(writes, func(i, j int) bool {
		return writes[i].Index < writes[j].Index
	})

	readEndpoints, err := endpointConfigsFromRows("read", reads, address)
	if err != nil {
		return endpointSelection{}, err
	}
	writeEndpoint, err := endpointConfigFromRow("write", writes[0], address)
	if err != nil {
		return endpointSelection{}, err
	}
	source := fmt.Sprintf("server-map:%s:%s", opts.Path, dataset)
	return finalizeEndpointSelection(endpointSelection{
		Source: source,
		Read:   readEndpoints,
		Write:  []endpointConfig{writeEndpoint},
	}), nil
}

func parseServerMapRows(raw []byte, dataset string, datacenter string) ([]serverMapRow, error) {
	var rows []serverMapRow
	currentDataset := ""
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	lineNo := 0
	rowIndex := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if match := serverMapDatasetPattern.FindStringSubmatch(line); match != nil {
			currentDataset = match[1]
			continue
		}
		if currentDataset != dataset || !strings.Contains(line, "array(") {
			continue
		}
		fields, ok, err := parseServerMapArrayLine(line)
		if err != nil {
			return nil, fmt.Errorf("parse db server map line %d: %w", lineNo, err)
		}
		if !ok {
			continue
		}
		if len(fields) != serverMapFieldCount {
			continue
		}
		readOrder, err := strconv.Atoi(strings.TrimSpace(fields[serverMapRead]))
		if err != nil {
			return nil, fmt.Errorf("parse read flag on line %d: %w", lineNo, err)
		}
		writeFlag, err := strconv.Atoi(strings.TrimSpace(fields[serverMapWrite]))
		if err != nil {
			return nil, fmt.Errorf("parse write flag on line %d: %w", lineNo, err)
		}
		dc := strings.TrimSpace(fields[serverMapDatacenter])
		rowIndex++
		rows = append(rows, serverMapRow{
			Datacenter: dc,
			ReadOrder:  readOrder,
			Write:      writeFlag > 0,
			Internet:   strings.TrimSpace(fields[serverMapInternet]),
			Internal:   strings.TrimSpace(fields[serverMapInternal]),
			Database:   strings.TrimSpace(fields[serverMapDBName]),
			User:       strings.TrimSpace(fields[serverMapUser]),
			Password:   fields[serverMapPassword],
			Index:      rowIndex,
			Local:      datacenter != "" && strings.Contains(strings.ToLower(dc), datacenter),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func parseServerMapArrayLine(line string) ([]string, bool, error) {
	start := strings.Index(line, "array(")
	if start == -1 {
		return nil, false, nil
	}
	start += len("array(")
	end := strings.LastIndex(line, ")")
	if end <= start {
		return nil, false, nil
	}
	fields, err := splitPHPArrayFields(line[start:end])
	if err != nil {
		return nil, true, err
	}
	return fields, true, nil
}

func splitPHPArrayFields(in string) ([]string, error) {
	var fields []string
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range in {
		if quote != 0 {
			if escaped {
				b.WriteRune(r)
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ',':
			fields = append(fields, normalizePHPArrayField(b.String()))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted field")
	}
	fields = append(fields, normalizePHPArrayField(b.String()))
	return fields, nil
}

func normalizePHPArrayField(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "null") {
		return ""
	}
	return s
}

func endpointConfigsFromRows(role string, rows []serverMapRow, address string) ([]endpointConfig, error) {
	out := make([]endpointConfig, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		ep, err := endpointConfigFromRow(role, row, address)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[ep.signature]; exists {
			continue
		}
		seen[ep.signature] = struct{}{}
		out = append(out, ep)
	}
	return out, nil
}

func endpointConfigFromRow(role string, row serverMapRow, address string) (endpointConfig, error) {
	addr := row.Internet
	if address == "internal" {
		addr = row.Internal
	}
	host, port, err := splitDBHostPort(addr)
	if err != nil {
		return endpointConfig{}, fmt.Errorf("%s endpoint %q: %w", role, addr, err)
	}
	return endpointFromParts(role, host, port, row.Database, row.User, row.Password)
}

func endpointFromParts(role, host, port, database, user, password string) (endpointConfig, error) {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	database = strings.TrimSpace(database)
	user = strings.TrimSpace(user)
	if host == "" || port == "" || database == "" || user == "" {
		return endpointConfig{}, fmt.Errorf("%s db endpoint requires host, port, database, and user", role)
	}
	mc := mysql.NewConfig()
	mc.User = user
	mc.Passwd = password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(host, port)
	mc.DBName = database
	mc.ParseTime = true
	mc.Timeout = 10 * time.Second
	mc.ReadTimeout = 30 * time.Second
	mc.WriteTimeout = 30 * time.Second
	mc.RejectReadOnly = role == "write"
	ep := endpointConfig{
		Label:    fmt.Sprintf("%s:%s/%s", host, port, database),
		Host:     host,
		Port:     port,
		Database: database,
		User:     user,
		Password: password,
		mysql:    mc,
	}
	ep.signature = endpointSignature(ep)
	return ep, nil
}

func splitDBHostPort(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", fmt.Errorf("empty address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		return host, port, nil
	}
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 || idx == len(addr)-1 {
		return "", "", err
	}
	return addr[:idx], addr[idx+1:], nil
}

func finalizeEndpointSelection(sel endpointSelection) endpointSelection {
	var parts []string
	parts = append(parts, "source="+sel.Source)
	for _, ep := range sel.Read {
		parts = append(parts, "read="+ep.signature)
	}
	for _, ep := range sel.Write {
		parts = append(parts, "write="+ep.signature)
	}
	sel.Signature = strings.Join(parts, "|")
	return sel
}

func endpointSignature(ep endpointConfig) string {
	return strings.Join([]string{ep.Host, ep.Port, ep.Database, ep.User, ep.Password}, "\x00")
}

func isBackupDatacenter(dc string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(dc)), "bak")
}

func inferDatacenterFromHostname(hostname string) string {
	parts := strings.Split(hostname, ".")
	if len(parts) < 4 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parts[len(parts)-3]))
}
