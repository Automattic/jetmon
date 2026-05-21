package db

import (
	"strings"
	"testing"

	"github.com/Automattic/jetmon/internal/config"
)

func TestLoadEndpointSelectionUsesExplicitConfigDSN(t *testing.T) {
	sel, err := loadEndpointSelection(&config.DBConfig{
		Host:     "db.local",
		Port:     "3307",
		Name:     "jetmon_db",
		User:     "jetmon",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("loadEndpointSelection: %v", err)
	}
	if sel.Source != "config:DB_HOST" {
		t.Fatalf("Source = %q, want config:DB_HOST", sel.Source)
	}
	if got := len(sel.Read); got != 1 {
		t.Fatalf("read endpoints = %d, want 1", got)
	}
	if sel.Read[0].Host != "db.local" || sel.Read[0].Port != "3307" {
		t.Fatalf("read endpoint = %s:%s, want db.local:3307", sel.Read[0].Host, sel.Read[0].Port)
	}
	if sel.Write[0].User != "jetmon" {
		t.Fatalf("write user = %q, want jetmon", sel.Write[0].User)
	}
}

func TestParseServerMapSelectionPrefersLocalReadAndWriteMaster(t *testing.T) {
	raw := []byte(`<?php
$db_servers = array(
	'blog_1' => array(
		array( 'dfw', 1, 0, 'blog-read:3306', 'blog-read.lan:3306', 'blog', 'blog_user', 'blog_pass', null, null, 30 ),
	),
	'misc' => array(
		array( 'dfw', 0, 1, 'misc-write:3306', 'misc-write.lan:3306', 'misc', 'misc_user', 'write,pass', null, null, 30 ),
		array( 'dfw', 1, 0, 'misc-dfw-a:3306', 'misc-dfw-a.lan:3306', 'misc', 'misc_user', 'read_pass', null, null, 30 ),
		array( 'dca', 1, 0, 'misc-dca-a:3306', 'misc-dca-a.lan:3306', 'misc', 'misc_user', 'read_pass', null, null, 30 ),
		array( 'bur', 2, 0, 'misc-bur-a:3306', 'misc-bur-a.lan:3306', 'misc', 'misc_user', 'read_pass', null, null, 30 ),
		array( 'bak', 1, 0, 'misc-bak-a:3306', 'misc-bak-a.lan:3306', 'misc', 'misc_user', 'read_pass', null, null, 30 ),
	),
)
`)

	sel, err := parseServerMapSelection(raw, serverMapOptions{
		Dataset:    "misc",
		Datacenter: "dca",
		Address:    "internet",
		Path:       "/private/db-servers.php",
	})
	if err != nil {
		t.Fatalf("parseServerMapSelection: %v", err)
	}
	if got, want := len(sel.Write), 1; got != want {
		t.Fatalf("write endpoints = %d, want %d", got, want)
	}
	if got, want := sel.Write[0].Host, "misc-write"; got != want {
		t.Fatalf("write host = %q, want %q", got, want)
	}
	if got, want := len(sel.Read), 3; got != want {
		t.Fatalf("read endpoints = %d, want %d", got, want)
	}
	wantReadHosts := []string{"misc-dca-a", "misc-dfw-a", "misc-bur-a"}
	for i, want := range wantReadHosts {
		if got := sel.Read[i].Host; got != want {
			t.Fatalf("read[%d] host = %q, want %q", i, got, want)
		}
	}
	if strings.Contains(sel.Signature, "blog-read") {
		t.Fatalf("signature included non-target dataset: %s", sel.Signature)
	}
}

func TestParseServerMapSelectionUsesInternalAddressWhenRequested(t *testing.T) {
	raw := []byte(`<?php
$db_servers = array(
	'misc' => array(
		array( 'dfw', 1, 1, 'misc-public:3306', 'misc-private.lan:3307', 'misc', 'misc_user', 'misc_pass', null, null, 30 ),
	),
)
`)
	sel, err := parseServerMapSelection(raw, serverMapOptions{
		Dataset: "misc",
		Address: "internal",
		Path:    "/private/db-servers.php",
	})
	if err != nil {
		t.Fatalf("parseServerMapSelection: %v", err)
	}
	if got, want := sel.Read[0].Host, "misc-private.lan"; got != want {
		t.Fatalf("read host = %q, want %q", got, want)
	}
	if got, want := sel.Read[0].Port, "3307"; got != want {
		t.Fatalf("read port = %q, want %q", got, want)
	}
}

func TestParseServerMapSelectionFallsBackToWriteWhenNoReadRowsExist(t *testing.T) {
	raw := []byte(`<?php
$db_servers = array(
	'misc' => array(
		array( 'dfw', 0, 1, 'misc-write:3306', 'misc-write.lan:3306', 'misc', 'misc_user', 'misc_pass', null, null, 30 ),
	),
)
`)
	sel, err := parseServerMapSelection(raw, serverMapOptions{
		Dataset: "misc",
		Address: "internet",
		Path:    "/private/db-servers.php",
	})
	if err != nil {
		t.Fatalf("parseServerMapSelection: %v", err)
	}
	if got, want := len(sel.Read), 1; got != want {
		t.Fatalf("read endpoints = %d, want %d", got, want)
	}
	if got, want := sel.Read[0].Host, "misc-write"; got != want {
		t.Fatalf("read host = %q, want %q", got, want)
	}
}

func TestEndpointFromPartsPinsUTCTimeZone(t *testing.T) {
	ep, err := endpointFromParts("write", "db.example", "3306", "jetmon", "user", "pass")
	if err != nil {
		t.Fatalf("endpointFromParts: %v", err)
	}
	if ep.mysql == nil {
		t.Fatal("endpoint mysql config is nil")
	}
	if !ep.mysql.ParseTime {
		t.Error("ParseTime = false, want true")
	}
	if ep.mysql.Loc == nil || ep.mysql.Loc.String() != "UTC" {
		t.Errorf("Loc = %v, want UTC", ep.mysql.Loc)
	}
	if got := ep.mysql.Params["time_zone"]; got != "'+00:00'" {
		t.Errorf("time_zone param = %q, want '+00:00'", got)
	}
	// The session time zone must survive into the actual DSN the driver dials.
	dsn := ep.mysql.FormatDSN()
	if !strings.Contains(dsn, "time_zone=%27%2B00%3A00%27") {
		t.Errorf("FormatDSN did not encode the UTC time_zone param: %s", dsn)
	}
	if !strings.Contains(dsn, "parseTime=true") {
		t.Errorf("FormatDSN missing parseTime: %s", dsn)
	}
}
