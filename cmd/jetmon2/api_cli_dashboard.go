package main

import (
	"context"
	"fmt"
	"net/http"
)

func cmdAPIDashboard(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: jetmon2 api dashboard <state|health|host|fleet> [flags]")
	}

	sub := args[0]
	rest := args[1:]
	path := ""
	switch sub {
	case "state":
		path = "/api/v1/dashboard/state"
	case "health":
		path = "/api/v1/dashboard/health"
	case "host":
		path = "/api/v1/dashboard/host"
	case "fleet":
		path = "/api/v1/dashboard/fleet"
	default:
		return fmt.Errorf("unknown api dashboard subcommand %q (want: state, health, host, fleet)", sub)
	}

	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api dashboard "+sub, &opts)
	if err := parseAPIFlags(fs, rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: jetmon2 api dashboard %s [flags]", sub)
	}
	return executeAPIDashboardPath(path, nil, opts)
}

func executeAPIDashboardRequest(sub string, client *http.Client, opts apiCLIOptions) error {
	switch sub {
	case "state":
		return executeAPIDashboardPath("/api/v1/dashboard/state", client, opts)
	case "health":
		return executeAPIDashboardPath("/api/v1/dashboard/health", client, opts)
	case "host":
		return executeAPIDashboardPath("/api/v1/dashboard/host", client, opts)
	case "fleet":
		return executeAPIDashboardPath("/api/v1/dashboard/fleet", client, opts)
	default:
		return fmt.Errorf("unknown api dashboard subcommand %q (want: state, health, host, fleet)", sub)
	}
}

func executeAPIDashboardPath(path string, client *http.Client, opts apiCLIOptions) error {
	return executeAPIRequest(context.Background(), client, opts, http.MethodGet, path, nil)
}
