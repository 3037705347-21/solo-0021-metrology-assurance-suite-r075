package assurance_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/assurance"
	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

func newService() (*assurance.Service, context.Context) {
	return assurance.NewService(repository.New()), context.Background()
}

func registerDevice(t *testing.T, ctx context.Context, service *assurance.Service, tag, room string) string {
	t.Helper()
	device, err := service.RegisterDevice(ctx, contracts.RegisterDeviceRequest{
		AssetTag: tag,
		Model:    "Bench Meter",
		Room:     room,
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	return device.ID
}

func openCase(t *testing.T, ctx context.Context, service *assurance.Service, deviceID string) string {
	t.Helper()
	view, err := service.OpenCase(ctx, contracts.OpenCaseRequest{
		DeviceID:  deviceID,
		Nominal:   25.0,
		Tolerance: 0.5,
		Steward:   "Qiao",
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}
	return view.Case.ID
}

func recordMeasurement(t *testing.T, ctx context.Context, service *assurance.Service, caseID string, observed float64, technician string) contracts.CaseResponse {
	t.Helper()
	view, err := service.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{
		Observed:   observed,
		Technician: technician,
	})
	if err != nil {
		t.Fatalf("record measurement: %v", err)
	}
	return view
}

// A measurement is pinned to the room the device was in at submission time.
// Relocation affects only later measurements; earlier measurements keep their
// room both in the write response and on every later read, including after
// sealing.
func TestMeasurementRoomsArePinnedAcrossRelocationAndSeal(t *testing.T) {
	service, ctx := newService()
	deviceID := registerDevice(t, ctx, service, "MET-3001", "Lab A")
	caseID := openCase(t, ctx, service, deviceID)

	first := recordMeasurement(t, ctx, service, caseID, 26.5, "Rui")
	if first.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("first measurement state = %q, want recheck_required", first.Case.State)
	}
	if first.Measurements[0].Room != "Lab A" {
		t.Fatalf("first measurement room = %q, want Lab A", first.Measurements[0].Room)
	}

	moved, err := service.RecordDeployment(ctx, deviceID, contracts.RecordDeploymentRequest{
		Room:   "Lab B",
		Actor:  "Lin",
		Reason: "device moved to the new wing",
	})
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if moved.Device.Room != "Lab B" {
		t.Fatalf("device room after relocation = %q, want Lab B", moved.Device.Room)
	}
	if len(moved.Deployment.ID) == 0 {
		t.Fatal("successful relocation did not return a deployment record")
	}

	// The case is review_ready after the accepted measurement, so it must be
	// reopened before a further measurement can be recorded.
	reopened, err := service.ReopenCase(ctx, caseID, contracts.ReopenCaseRequest{
		Actor:  "Lin",
		Reason: "second reading required in the new room",
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Case.State != domain.CaseAwaitingMeasurement {
		t.Fatalf("case state = %q, want awaiting_measurement", reopened.Case.State)
	}

	second := recordMeasurement(t, ctx, service, caseID, 24.9, "Rui")
	if len(second.Measurements) != 2 {
		t.Fatalf("measurement count = %d, want 2", len(second.Measurements))
	}
	if second.Measurements[0].Room != "Lab A" {
		t.Fatalf("first measurement room after relocation = %q, want Lab A", second.Measurements[0].Room)
	}
	if second.Measurements[1].Room != "Lab B" {
		t.Fatalf("second measurement room = %q, want Lab B", second.Measurements[1].Room)
	}

	// A fresh read must return the same pinned rooms, not the device's current
	// room rewritten onto historical measurements.
	reread, err := service.Case(ctx, caseID)
	if err != nil {
		t.Fatalf("read case: %v", err)
	}
	if reread.Measurements[0].Room != "Lab A" || reread.Measurements[1].Room != "Lab B" {
		t.Fatalf("reread rooms = %q, %q; want Lab A, Lab B", reread.Measurements[0].Room, reread.Measurements[1].Room)
	}

	sealed, err := service.SealCase(ctx, caseID, contracts.SealCaseRequest{
		Reviewer: "Wen",
		Summary:  "accepted measurement sealed for review",
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealed.Case.State != domain.CaseSealed {
		t.Fatalf("case state = %q, want sealed", sealed.Case.State)
	}

	// One more relocation after sealing must not touch the sealed evidence.
	if _, err := service.RecordDeployment(ctx, deviceID, contracts.RecordDeploymentRequest{
		Room:   "Lab C",
		Actor:  "Lin",
		Reason: "equipment reorganization",
	}); err != nil {
		t.Fatalf("second relocation: %v", err)
	}
	afterSeal, err := service.Case(ctx, caseID)
	if err != nil {
		t.Fatalf("read sealed case: %v", err)
	}
	if afterSeal.Measurements[0].Room != "Lab A" || afterSeal.Measurements[1].Room != "Lab B" {
		t.Fatalf("sealed measurement rooms were rewritten: %q, %q", afterSeal.Measurements[0].Room, afterSeal.Measurements[1].Room)
	}
	if afterSeal.Seal == nil {
		t.Fatal("evidence seal disappeared")
	}

	device, err := service.Device(ctx, deviceID)
	if err != nil {
		t.Fatalf("read device: %v", err)
	}
	if device.Room != "Lab C" {
		t.Fatalf("device current room = %q, want Lab C", device.Room)
	}
}

// A relocation rejected for incomplete room/actor/reason information must
// leave no trace: device room, deployment history, and later valid retry must
// all behave as if the request never happened.
func TestRejectedRelocationLeavesNoTrace(t *testing.T) {
	service, ctx := newService()
	deviceID := registerDevice(t, ctx, service, "MET-3002", "Lab A")

	cases := []struct {
		name  string
		input contracts.RecordDeploymentRequest
	}{
		{"missing room", contracts.RecordDeploymentRequest{Actor: "Lin", Reason: "moved"}},
		{"missing actor", contracts.RecordDeploymentRequest{Room: "Lab B", Reason: "moved"}},
		{"missing reason", contracts.RecordDeploymentRequest{Room: "Lab B", Actor: "Lin"}},
		{"all missing", contracts.RecordDeploymentRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.RecordDeployment(ctx, deviceID, tc.input)
			if err == nil || !strings.Contains(err.Error(), "room, actor, and reason are required") {
				t.Fatalf("relocation error = %v, want invalid input", err)
			}
		})
	}

	device, err := service.Device(ctx, deviceID)
	if err != nil {
		t.Fatalf("read device: %v", err)
	}
	if device.Room != "Lab A" {
		t.Fatalf("device room after rejected relocations = %q, want Lab A", device.Room)
	}

	history, err := service.Deployments(ctx, deviceID)
	if err != nil {
		t.Fatalf("read deployments: %v", err)
	}
	if len(history) != 1 || history[0].Room != "Lab A" {
		t.Fatalf("deployment history after rejected relocations = %+v, want only enrollment", history)
	}

	// The correct retry must succeed; previously the rejected request had
	// already moved the device and this retry failed with a state conflict.
	retry, err := service.RecordDeployment(ctx, deviceID, contracts.RecordDeploymentRequest{
		Room:   "Lab B",
		Actor:  "Lin",
		Reason: "moved to the new wing",
	})
	if err != nil {
		t.Fatalf("valid relocation retry: %v", err)
	}
	if retry.Device.Room != "Lab B" {
		t.Fatalf("device room after retry = %q, want Lab B", retry.Device.Room)
	}
}

// Relocating to the current room is a state conflict and changes nothing.
func TestRelocationToSameRoomIsConflict(t *testing.T) {
	service, ctx := newService()
	deviceID := registerDevice(t, ctx, service, "MET-3003", "Lab A")

	_, err := service.RecordDeployment(ctx, deviceID, contracts.RecordDeploymentRequest{
		Room:   "Lab A",
		Actor:  "Lin",
		Reason: "paperwork correction",
	})
	if err == nil {
		t.Fatal("relocation to the same room was accepted")
	}

	device, err := service.Device(ctx, deviceID)
	if err != nil {
		t.Fatalf("read device: %v", err)
	}
	if device.Room != "Lab A" {
		t.Fatalf("device room = %q, want Lab A", device.Room)
	}
	history, err := service.Deployments(ctx, deviceID)
	if err != nil {
		t.Fatalf("read deployments: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("deployment history = %d entries, want 1 (enrollment only)", len(history))
	}
}

func TestRelocationValidationOrder(t *testing.T) {
	service, ctx := newService()
	registerDevice(t, ctx, service, "MET-3004", "Lab A")

	// Incomplete input is rejected before device lookup, and a well-formed
	// request for an unknown device is a not-found error.
	_, err := service.RecordDeployment(ctx, "missing-device", contracts.RecordDeploymentRequest{})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty relocation error = %v, want invalid input", err)
	}
	_, err = service.RecordDeployment(ctx, "missing-device", contracts.RecordDeploymentRequest{
		Room: "Lab B", Actor: "Lin", Reason: "moved",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("relocation of unknown device error = %v, want not found", err)
	}
}
