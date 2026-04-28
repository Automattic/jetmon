package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/eventstore"
	"github.com/DATA-DOG/go-sqlmock"
)

var contactColumns = []string{
	"id", "label", "active", "transport", "destination_preview",
	"site_filter", "min_severity", "max_per_hour",
	"created_by", "created_at", "updated_at",
}

func contactRow(id int64, label string, active uint8, transport Transport, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(contactColumns).AddRow(
		id, label, active, string(transport), "mple",
		`{"site_ids":[42]}`, uint8(eventstore.SeverityDown), 60,
		"ops", now, now,
	)
}

func TestCreateContactPersistsDefaultsAndFetchesRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	destination := json.RawMessage(`{"address":"ops@example.com"}`)
	mock.ExpectExec("INSERT INTO jetmon_alert_contacts").
		WithArgs(
			"Ops email", 1, string(TransportEmail), []byte(destination), ".com",
			sqlmock.AnyArg(), uint8(eventstore.SeverityDown), 60, "ops",
		).
		WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery("SELECT id, label, active, transport").
		WithArgs(int64(11)).
		WillReturnRows(contactRow(11, "Ops email", 1, TransportEmail, now))

	contact, err := Create(context.Background(), db, CreateInput{
		Label:       "Ops email",
		Transport:   TransportEmail,
		Destination: destination,
		SiteFilter:  SiteFilter{SiteIDs: []int64{42}},
		CreatedBy:   "ops",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if contact.ID != 11 || !contact.Active || contact.SiteFilter.SiteIDs[0] != 42 {
		t.Fatalf("contact = %+v", contact)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateContactRejectsInvalidInputBeforeDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	if _, err := Create(context.Background(), db, CreateInput{}); err == nil {
		t.Fatal("Create accepted empty label and destination")
	}
	if _, err := Create(context.Background(), db, CreateInput{
		Label:       "Ops",
		Transport:   Transport("sms"),
		Destination: json.RawMessage(`{"address":"ops@example.com"}`),
	}); !errors.Is(err, ErrInvalidTransport) {
		t.Fatalf("Create invalid transport error = %v, want ErrInvalidTransport", err)
	}
	sev := uint8(99)
	if _, err := Create(context.Background(), db, CreateInput{
		Label:       "Ops",
		Transport:   TransportEmail,
		Destination: json.RawMessage(`{"address":"ops@example.com"}`),
		MinSeverity: &sev,
	}); !errors.Is(err, ErrInvalidSeverity) {
		t.Fatalf("Create invalid severity error = %v, want ErrInvalidSeverity", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected sql calls: %v", err)
	}
}

func TestDestinationPreviewByTransport(t *testing.T) {
	cases := []struct {
		transport Transport
		dest      json.RawMessage
		want      string
	}{
		{TransportEmail, json.RawMessage(`{"address":"ops@example.com"}`), ".com"},
		{TransportPagerDuty, json.RawMessage(`{"integration_key":"abcd1234"}`), "1234"},
		{TransportSlack, json.RawMessage(`{"webhook_url":"https://hooks.slack.test/XYZ"}`), "/XYZ"},
		{TransportTeams, json.RawMessage(`{"webhook_url":"https://teams.test/ABCD"}`), "ABCD"},
	}
	for _, tc := range cases {
		if err := validateDestination(tc.transport, tc.dest); err != nil {
			t.Fatalf("validateDestination(%s): %v", tc.transport, err)
		}
		if got := destinationPreview(tc.transport, tc.dest); got != tc.want {
			t.Fatalf("destinationPreview(%s) = %q, want %q", tc.transport, got, tc.want)
		}
	}
}

func TestGetContactNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT id, label, active, transport").
		WithArgs(int64(404)).
		WillReturnError(sql.ErrNoRows)

	_, err = Get(context.Background(), db, 404)
	if !errors.Is(err, ErrContactNotFound) {
		t.Fatalf("Get error = %v, want ErrContactNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListContactsScansRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	rows := sqlmock.NewRows(contactColumns).
		AddRow(int64(1), "Email", uint8(1), string(TransportEmail), "mple", `{}`, uint8(4), 60, "ops", now, now).
		AddRow(int64(2), "Slack", uint8(0), string(TransportSlack), "hook", nil, uint8(3), 0, "ops", now, now)
	mock.ExpectQuery("SELECT id, label, active, transport").
		WillReturnRows(rows)

	contacts, err := List(context.Background(), db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(contacts) != 2 || !contacts[0].Active || contacts[1].Active {
		t.Fatalf("contacts = %+v", contacts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListActiveContactsScansRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, label, active, transport").
		WillReturnRows(contactRow(3, "PagerDuty", 1, TransportPagerDuty, now))

	contacts, err := ListActive(context.Background(), db)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(contacts) != 1 || contacts[0].Transport != TransportPagerDuty {
		t.Fatalf("contacts = %+v", contacts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpdateContactAppliesPatchAndFetchesRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	label := "Escalation"
	active := false
	destination := json.RawMessage(`{"address":"new@example.com"}`)
	siteFilter := SiteFilter{SiteIDs: []int64{7}}
	minSeverity := uint8(eventstore.SeverityWarning)
	maxPerHour := 5

	mock.ExpectQuery("SELECT id, label, active, transport").
		WithArgs(int64(5)).
		WillReturnRows(contactRow(5, "Ops email", 1, TransportEmail, now))
	mock.ExpectExec("UPDATE jetmon_alert_contacts SET").
		WithArgs(label, 0, []byte(destination), ".com", sqlmock.AnyArg(), minSeverity, maxPerHour, int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, label, active, transport").
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows(contactColumns).AddRow(
			int64(5), label, uint8(0), string(TransportEmail), ".com",
			`{"site_ids":[7]}`, minSeverity, maxPerHour, "ops", now, now,
		))

	contact, err := Update(context.Background(), db, 5, UpdateInput{
		Label:       &label,
		Active:      &active,
		Destination: destination,
		SiteFilter:  &siteFilter,
		MinSeverity: &minSeverity,
		MaxPerHour:  &maxPerHour,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if contact.Active || contact.Label != label || contact.SiteFilter.SiteIDs[0] != 7 {
		t.Fatalf("contact = %+v", contact)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestDeleteContactReportsMissingRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("DELETE FROM jetmon_alert_contacts").
		WithArgs(int64(10)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := Delete(context.Background(), db, 10); !errors.Is(err, ErrContactNotFound) {
		t.Fatalf("Delete error = %v, want ErrContactNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestLoadDestination(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT destination FROM jetmon_alert_contacts").
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"destination"}).AddRow([]byte(`{"address":"ops@example.com"}`)))

	dest, err := LoadDestination(context.Background(), db, 4)
	if err != nil {
		t.Fatalf("LoadDestination: %v", err)
	}
	if !strings.Contains(string(dest), "ops@example.com") {
		t.Fatalf("destination = %s", dest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

var alertDeliveryColumns = []string{
	"id", "alert_contact_id", "transition_id", "event_id", "event_type", "severity",
	"payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response",
	"last_attempt_at", "delivered_at", "created_at",
}

func alertDeliveryRow(id int64, status Status, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(alertDeliveryColumns).AddRow(
		id, int64(20), int64(30), int64(40), "alert.opened", uint8(4),
		[]byte(`{"ok":true}`), string(status), 2, now, 503, "down", now, nil, now,
	)
}

func TestEnqueueAlertDeliveryReturnsInsertedIDAndDuplicateZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	payload := json.RawMessage(`{"type":"alert.opened"}`)
	mock.ExpectExec("INSERT IGNORE INTO jetmon_alert_deliveries").
		WithArgs(int64(1), int64(2), int64(3), "alert.opened", uint8(4), []byte(payload)).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec("INSERT IGNORE INTO jetmon_alert_deliveries").
		WithArgs(int64(1), int64(2), int64(3), "alert.opened", uint8(4), []byte(payload)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	id, err := Enqueue(context.Background(), db, EnqueueInput{
		AlertContactID: 1, TransitionID: 2, EventID: 3, EventType: "alert.opened",
		Severity: 4, Payload: payload,
	})
	if err != nil || id != 9 {
		t.Fatalf("Enqueue inserted = (%d, %v), want (9, nil)", id, err)
	}
	id, err = Enqueue(context.Background(), db, EnqueueInput{
		AlertContactID: 1, TransitionID: 2, EventID: 3, EventType: "alert.opened",
		Severity: 4, Payload: payload,
	})
	if err != nil || id != 0 {
		t.Fatalf("Enqueue duplicate = (%d, %v), want (0, nil)", id, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestAlertDeliveryStateUpdates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	next := time.Now().UTC().Add(time.Minute)
	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs(204, "ok", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs("quiet", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs(503, "retry", next, int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs(410, "gone", int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := MarkDelivered(context.Background(), db, 1, 204, "ok"); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if err := MarkSuppressed(context.Background(), db, 2, "quiet"); err != nil {
		t.Fatalf("MarkSuppressed: %v", err)
	}
	if err := ScheduleRetry(context.Background(), db, 3, 503, "retry", next, false); err != nil {
		t.Fatalf("ScheduleRetry retry: %v", err)
	}
	if err := ScheduleRetry(context.Background(), db, 4, 410, "gone", next, true); err != nil {
		t.Fatalf("ScheduleRetry abandon: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetListAndRetryAlertDeliveries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, alert_contact_id, transition_id").
		WithArgs(int64(1)).
		WillReturnRows(alertDeliveryRow(1, StatusAbandoned, now))
	mock.ExpectQuery("SELECT id, alert_contact_id, transition_id").
		WithArgs(int64(20), string(StatusAbandoned), int64(50), 10).
		WillReturnRows(alertDeliveryRow(2, StatusAbandoned, now))
	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs(int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	d, err := GetDelivery(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("GetDelivery: %v", err)
	}
	if d.LastStatusCode == nil || *d.LastStatusCode != 503 || d.LastResponse == nil || *d.LastResponse != "down" {
		t.Fatalf("delivery did not scan nullable fields: %+v", d)
	}
	list, err := ListDeliveries(context.Background(), db, 20, StatusAbandoned, 50, 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(list) != 1 || list[0].ID != 2 {
		t.Fatalf("deliveries = %+v", list)
	}
	if err := RetryDelivery(context.Background(), db, 2); err != nil {
		t.Fatalf("RetryDelivery: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
