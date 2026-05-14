package orchestrator

import (
	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/eventstore"
)

func monitorTargetID(site db.Site) int64 {
	if site.ID > 0 {
		return site.ID
	}
	return site.BlogID
}

func checkResultTargetID(res checker.Result) int64 {
	if res.MonitorSiteID > 0 {
		return res.MonitorSiteID
	}
	return res.BlogID
}

func httpEventIdentity(site db.Site) eventstore.Identity {
	return eventIdentity(site, checkTypeHTTP)
}

func eventIdentity(site db.Site, checkType string) eventstore.Identity {
	identity := eventstore.Identity{BlogID: site.BlogID, CheckType: checkType}
	if site.ID > 0 {
		endpointID := site.ID
		identity.EndpointID = &endpointID
	}
	return identity
}
