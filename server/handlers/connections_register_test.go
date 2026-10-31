package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/meshery/meshery/server/machines"
	"github.com/meshery/meshery/server/models"
	"github.com/meshery/meshery/server/models/connections"
	"github.com/meshery/meshkit/database"
	"github.com/meshery/meshkit/models/events"
	"github.com/meshery/schemas/models/core"
	connectionv1beta3 "github.com/meshery/schemas/models/v1beta3/connection"
)

// The Create Connection wizard (genericRegisterStep) sends {"status":"register"}
// for every non-Kubernetes kind and only sends "connect" if that succeeds. Kinds
// without a verification step (github, artifacthub) used to get a 500 here,
// because the default machine had no REGISTERED action (meshery#22708).
//
// The connection row is saved up front: the handler does not yet persist it
// before driving the machine (meshery#21813), and without the row the
// post-transition status write fails with "record not found" regardless.
func TestProcessConnectionRegistrationRegisterStepWithoutVerifyAction(t *testing.T) {
	db, err := database.New(database.Options{Engine: database.SQLITE, Filename: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(connections.Connection{}, events.Event{}, models.Credential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	systemID := uuid.Must(uuid.NewV4())
	h := &Handler{
		config:   &models.HandlerConfig{EventBroadcaster: &models.Broadcast{}},
		log:      newTestLogger(t),
		SystemID: &systemID,
		ConnectionToStateMachineInstanceTracker: &machines.ConnectionToStateMachineInstanceTracker{
			ConnectToInstanceMap: make(map[core.Uuid]*machines.StateMachine),
		},
	}
	provider := &models.DefaultLocalProvider{
		ConnectionPersister: &models.ConnectionPersister{DB: &db},
		EventsPersister:     &models.EventsPersister{DB: &db},
		GenericPersister:    &db,
	}

	for _, kind := range []string{"github", "artifacthub"} {
		t.Run(kind, func(t *testing.T) {
			id := core.Uuid(uuid.Must(uuid.NewV4()))
			if _, err := provider.ConnectionPersister.SaveConnection(&connections.Connection{
				ID: id, Name: "my-" + kind, Kind: kind, Status: connections.DISCOVERED,
			}); err != nil {
				t.Fatalf("save connection: %v", err)
			}
			body, err := json.Marshal(connectionv1beta3.ConnectionRegistrationEvent{
				ID:                         &id,
				Kind:                       kind,
				Name:                       "my-" + kind,
				Status:                     connectionv1beta3.ConnectionRegistrationEventStatusRegister,
				SkipCredentialVerification: true,
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			user := &models.User{ID: uuid.Must(uuid.NewV4())}
			ctx := context.WithValue(context.Background(), models.UserCtxKey, user)
			ctx = context.WithValue(ctx, models.SystemIDKey, h.SystemID)
			req := httptest.NewRequest(http.MethodPost, "/api/integrations/connections/register", bytes.NewReader(body)).WithContext(ctx)
			rec := httptest.NewRecorder()

			h.ProcessConnectionRegistration(rec, req, nil, user, provider)

			if rec.Code != http.StatusOK {
				t.Fatalf("register returned HTTP %d, want %d: %s", rec.Code, http.StatusOK, bytes.TrimSpace(rec.Body.Bytes()))
			}
			inst, ok := h.ConnectionToStateMachineInstanceTracker.Get(id)
			if !ok {
				t.Fatalf("no state machine tracked for connection %s", id)
			}
			if inst.CurrentState != machines.REGISTERED {
				t.Fatalf("machine state = %s, want %s", inst.CurrentState, machines.REGISTERED)
			}
		})
	}
}
