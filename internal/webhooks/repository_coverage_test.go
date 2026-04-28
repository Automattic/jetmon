package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var webhookColumns = []string{
	"id", "url", "active", "events", "site_filter", "state_filter",
	"secret_preview", "created_by", "created_at", "updated_at",
}

func webhookRow(id int64, url string, active uint8, createdAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(webhookColumns).AddRow(
		id, url, active,
		`["event.opened"]`,
		`{"site_ids":[42]}`,
		`{"states":["Down"]}`,
		"_XYZ", "ops", createdAt, createdAt,
	)
}

func TestCreateWebhookPersistsDefaultsAndFetchesRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectExec("INSERT INTO jetmon_webhooks").
		WithArgs(
			"https://consumer.example/hook",
			1,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			"ops",
		).
		WillReturnResult(sqlmock.NewResult(12, 1))
	mock.ExpectQuery("SELECT id, url, active, events").
		WithArgs(int64(12)).
		WillReturnRows(webhookRow(12, "https://consumer.example/hook", 1, now))

	raw, hook, err := Create(context.Background(), db, CreateInput{
		URL:         "https://consumer.example/hook",
		Events:      []string{EventOpened},
		SiteFilter:  SiteFilter{SiteIDs: []int64{42}},
		StateFilter: StateFilter{States: []string{"Down"}},
		CreatedBy:   "ops",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(raw, SecretPrefix) {
		t.Fatalf("raw secret = %q, want %s prefix", raw, SecretPrefix)
	}
	if hook.ID != 12 || !hook.Active || hook.SiteFilter.SiteIDs[0] != 42 || hook.StateFilter.States[0] != "Down" {
		t.Fatalf("hook = %+v", hook)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateWebhookRejectsInvalidInputBeforeDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	if _, _, err := Create(context.Background(), db, CreateInput{}); err == nil {
		t.Fatal("Create accepted an empty URL")
	}
	if _, _, err := Create(context.Background(), db, CreateInput{
		URL:    "https://consumer.example/hook",
		Events: []string{"event.bogus"},
	}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Create invalid event error = %v, want ErrInvalidEvent", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected sql calls: %v", err)
	}
}

func TestGetWebhookNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT id, url, active, events").
		WithArgs(int64(404)).
		WillReturnError(sql.ErrNoRows)

	_, err = Get(context.Background(), db, 404)
	if !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("Get error = %v, want ErrWebhookNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListWebhooksScansRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	rows := sqlmock.NewRows(webhookColumns).
		AddRow(int64(1), "https://a.example", uint8(1), `[]`, `{}`, `{}`, "aaaa", "ops", now, now).
		AddRow(int64(2), "https://b.example", uint8(0), nil, nil, nil, "bbbb", "ops", now, now)
	mock.ExpectQuery("SELECT id, url, active, events").
		WillReturnRows(rows)

	hooks, err := List(context.Background(), db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(hooks) != 2 || hooks[0].Active != true || hooks[1].Active != false {
		t.Fatalf("hooks = %+v", hooks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListActiveWebhooksScansRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, url, active, events").
		WillReturnRows(webhookRow(3, "https://active.example", 1, now))

	hooks, err := ListActive(context.Background(), db)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(hooks) != 1 || hooks[0].ID != 3 {
		t.Fatalf("hooks = %+v", hooks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpdateWebhookAppliesPatchAndFetchesRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	url := "https://consumer.example/new"
	active := false
	events := []string{EventClosed}
	siteFilter := SiteFilter{SiteIDs: []int64{7}}
	stateFilter := StateFilter{States: []string{"Up"}}
	now := time.Now().UTC()

	mock.ExpectExec("UPDATE jetmon_webhooks SET").
		WithArgs(url, 0, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, url, active, events").
		WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows(webhookColumns).AddRow(
			int64(5), url, uint8(0), `["event.closed"]`,
			`{"site_ids":[7]}`, `{"states":["Up"]}`, "_NEW", "ops", now, now,
		))

	hook, err := Update(context.Background(), db, 5, UpdateInput{
		URL:         &url,
		Active:      &active,
		Events:      &events,
		SiteFilter:  &siteFilter,
		StateFilter: &stateFilter,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if hook.Active || hook.Events[0] != EventClosed || hook.SiteFilter.SiteIDs[0] != 7 {
		t.Fatalf("hook = %+v", hook)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestDeleteWebhookReportsMissingRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("DELETE FROM jetmon_webhooks").
		WithArgs(int64(10)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := Delete(context.Background(), db, 10); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("Delete error = %v, want ErrWebhookNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestRotateSecretUpdatesStoredSecret(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectExec("UPDATE jetmon_webhooks SET secret").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, url, active, events").
		WithArgs(int64(8)).
		WillReturnRows(webhookRow(8, "https://consumer.example/hook", 1, now))

	raw, hook, err := RotateSecret(context.Background(), db, 8)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if !strings.HasPrefix(raw, SecretPrefix) || hook.ID != 8 {
		t.Fatalf("RotateSecret returned raw=%q hook=%+v", raw, hook)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestLoadSecret(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT secret FROM jetmon_webhooks").
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"secret"}).AddRow("whsec_secret"))

	secret, err := LoadSecret(context.Background(), db, 4)
	if err != nil {
		t.Fatalf("LoadSecret: %v", err)
	}
	if secret != "whsec_secret" {
		t.Fatalf("secret = %q", secret)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

var webhookDeliveryColumns = []string{
	"id", "webhook_id", "transition_id", "event_id", "event_type",
	"payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response",
	"last_attempt_at", "delivered_at", "created_at",
}

func webhookDeliveryRow(id int64, status Status, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(webhookDeliveryColumns).AddRow(
		id, int64(20), int64(30), int64(40), EventOpened,
		[]byte(`{"ok":true}`), string(status), 2, now, 503, "down", now, nil, now,
	)
}

func TestEnqueueWebhookDeliveryReturnsInsertedIDAndDuplicateZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	payload := json.RawMessage(`{"type":"event.opened"}`)
	mock.ExpectExec("INSERT IGNORE INTO jetmon_webhook_deliveries").
		WithArgs(int64(1), int64(2), int64(3), EventOpened, []byte(payload)).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec("INSERT IGNORE INTO jetmon_webhook_deliveries").
		WithArgs(int64(1), int64(2), int64(3), EventOpened, []byte(payload)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	id, err := Enqueue(context.Background(), db, EnqueueInput{
		WebhookID: 1, TransitionID: 2, EventID: 3, EventType: EventOpened, Payload: payload,
	})
	if err != nil || id != 9 {
		t.Fatalf("Enqueue inserted = (%d, %v), want (9, nil)", id, err)
	}
	id, err = Enqueue(context.Background(), db, EnqueueInput{
		WebhookID: 1, TransitionID: 2, EventID: 3, EventType: EventOpened, Payload: payload,
	})
	if err != nil || id != 0 {
		t.Fatalf("Enqueue duplicate = (%d, %v), want (0, nil)", id, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestWebhookDeliveryStateUpdates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	next := time.Now().UTC().Add(time.Minute)
	mock.ExpectExec("UPDATE jetmon_webhook_deliveries").
		WithArgs(204, "ok", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_webhook_deliveries").
		WithArgs(503, "retry", next, int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_webhook_deliveries").
		WithArgs(410, "gone", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := MarkDelivered(context.Background(), db, 1, 204, "ok"); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if err := ScheduleRetry(context.Background(), db, 2, 503, "retry", next, false); err != nil {
		t.Fatalf("ScheduleRetry retry: %v", err)
	}
	if err := ScheduleRetry(context.Background(), db, 3, 410, "gone", next, true); err != nil {
		t.Fatalf("ScheduleRetry abandon: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetListAndRetryWebhookDeliveries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, webhook_id, transition_id").
		WithArgs(int64(1)).
		WillReturnRows(webhookDeliveryRow(1, StatusAbandoned, now))
	mock.ExpectQuery("SELECT id, webhook_id, transition_id").
		WithArgs(int64(20), string(StatusAbandoned), int64(50), 10).
		WillReturnRows(webhookDeliveryRow(2, StatusAbandoned, now))
	mock.ExpectExec("UPDATE jetmon_webhook_deliveries").
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
