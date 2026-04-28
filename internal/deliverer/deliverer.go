// Package deliverer owns outbound delivery worker wiring.
package deliverer

import (
	"database/sql"
	"log"

	"github.com/Automattic/jetmon/internal/alerting"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/webhooks"
)

// Config is the runtime wiring needed by the outbound deliverer.
type Config struct {
	DB          *sql.DB
	InstanceID  string
	Dispatchers map[alerting.Transport]alerting.Dispatcher
	Logger      *log.Logger
}

// Runtime holds the active delivery workers.
type Runtime struct {
	hookWorker  *webhooks.Worker
	alertWorker *alerting.Worker
	logger      *log.Logger
}

// Start launches webhook and alert-contact delivery workers.
func Start(cfg Config) *Runtime {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}

	hookWorker := webhooks.NewWorker(webhooks.WorkerConfig{
		DB:         cfg.DB,
		InstanceID: cfg.InstanceID,
	})
	hookWorker.Start()
	logger.Println("webhooks: delivery worker started")

	alertWorker := alerting.NewWorker(alerting.WorkerConfig{
		DB:          cfg.DB,
		InstanceID:  cfg.InstanceID,
		Dispatchers: cfg.Dispatchers,
	})
	alertWorker.Start()
	logger.Printf("alerting: delivery worker started (transports=%d)", len(cfg.Dispatchers))

	return &Runtime{
		hookWorker:  hookWorker,
		alertWorker: alertWorker,
		logger:      logger,
	}
}

// Stop drains both delivery workers.
func (r *Runtime) Stop() {
	if r == nil {
		return
	}
	if r.hookWorker != nil {
		r.hookWorker.Stop()
		r.logger.Println("webhooks: delivery worker stopped")
	}
	if r.alertWorker != nil {
		r.alertWorker.Stop()
		r.logger.Println("alerting: delivery worker stopped")
	}
}

// BuildAlertDispatchers constructs the per-transport Dispatcher map
// from runtime config. Always returns the three webhook-shaped
// transports (PagerDuty, Slack, Teams) because they have no per-instance
// config beyond the destination credential stored on each alert contact.
// Email is selected with EMAIL_TRANSPORT: "wpcom"/"smtp" wire the
// corresponding sender, and "stub" or empty falls back to log-only.
func BuildAlertDispatchers(cfg *config.Config) map[alerting.Transport]alerting.Dispatcher {
	out := map[alerting.Transport]alerting.Dispatcher{
		alerting.TransportPagerDuty: &alerting.PagerDutyDispatcher{},
		alerting.TransportSlack:     &alerting.SlackDispatcher{},
		alerting.TransportTeams:     &alerting.TeamsDispatcher{},
	}

	var sender alerting.Sender
	switch cfg.EmailTransport {
	case "wpcom":
		sender = &alerting.WPCOMSender{
			Endpoint:  cfg.WPCOMEmailEndpoint,
			AuthToken: cfg.WPCOMEmailAuthToken,
		}
		log.Printf("alerting/email: using wpcom sender (endpoint=%s)", cfg.WPCOMEmailEndpoint)
	case "smtp":
		sender = &alerting.SMTPSender{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			UseTLS:   cfg.SMTPUseTLS,
		}
		log.Printf("alerting/email: using smtp sender (%s:%d)", cfg.SMTPHost, cfg.SMTPPort)
	default:
		sender = &alerting.StubSender{}
		log.Println("alerting/email: using stub sender (set EMAIL_TRANSPORT to enable real delivery)")
	}
	out[alerting.TransportEmail] = alerting.NewEmailDispatcher(sender, cfg.EmailFrom)
	return out
}
