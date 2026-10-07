package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type client struct {
	t      *testing.T
	server *httptest.Server
}

func newClient(t *testing.T) *client {
	t.Helper()
	server := httptest.NewServer(bootstrap.NewHandler())
	t.Cleanup(server.Close)
	return &client{t: t, server: server}
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, c.server.URL+path, reader)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.server.Client().Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		c.t.Fatalf("decode response: %v", err)
	}
	return response.StatusCode, decoded
}

func digString(t *testing.T, body map[string]any, path ...any) string {
	t.Helper()
	var current any = body
	for _, part := range path {
		switch key := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("%v is not an object while looking for %v", current, path)
			}
			current, ok = object[key]
			if !ok {
				t.Fatalf("missing key %q in %v", key, path)
			}
		case int:
			items, ok := current.([]any)
			if !ok || key < 0 || key >= len(items) {
				t.Fatalf("index %d is out of range while looking for %v", key, path)
			}
			current = items[key]
		default:
			t.Fatalf("unsupported path element %T", part)
		}
	}
	text, ok := current.(string)
	if !ok {
		t.Fatalf("%v is not a string", path)
	}
	return text
}

func (c *client) createDevice(tag, room string) string {
	status, body := c.do(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      room,
		"steward":   "Lin",
	})
	if status != http.StatusCreated {
		c.t.Fatalf("create device status = %d, body = %v", status, body)
	}
	return digString(c.t, body, "device", "id")
}

func (c *client) openCase(deviceID string) string {
	status, body := c.do(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if status != http.StatusCreated {
		c.t.Fatalf("open case status = %d, body = %v", status, body)
	}
	return digString(c.t, body, "case", "id")
}

func (c *client) relocate(deviceID, room, actor, reason string) (int, map[string]any) {
	return c.do(http.MethodPost, "/devices/"+deviceID+"/deployments", map[string]any{
		"room": room, "actor": actor, "reason": reason,
	})
}

// End-to-end: a rejected relocation returns 400 and changes nothing, the
// corrected relocation returns 201, and measurements before/after it carry
// the room that was valid when each was submitted. A sealed case keeps those
// rooms through a later relocation.
func TestRelocationAndMeasurementChain(t *testing.T) {
	api := newClient(t)
	deviceID := api.createDevice("MET-4001", "Lab A")
	caseID := api.openCase(deviceID)

	status, rejected := api.relocate(deviceID, "Lab B", "Lin", "")
	if status != http.StatusBadRequest {
		t.Fatalf("incomplete relocation status = %d, body = %v", status, rejected)
	}
	if code := digString(t, rejected, "error", "code"); code != "invalid_input" {
		t.Fatalf("error code = %q, want invalid_input", code)
	}

	status, body := api.do(http.MethodGet, "/devices/"+deviceID, nil)
	if status != http.StatusOK {
		t.Fatalf("get device status = %d", status)
	}
	if room := digString(t, body, "device", "room"); room != "Lab A" {
		t.Fatalf("device room after rejected relocation = %q, want Lab A", room)
	}
	status, history := api.do(http.MethodGet, "/devices/"+deviceID+"/deployments", nil)
	if status != http.StatusOK {
		t.Fatalf("list deployments status = %d", status)
	}
	items, ok := history["deployments"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("deployment history = %v, want exactly the enrollment entry", history["deployments"])
	}

	// First measurement while still in Lab A.
	status, measured := api.do(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed": 25.2, "technician": "Rui",
	})
	if status != http.StatusCreated {
		t.Fatalf("first measurement status = %d, body = %v", status, measured)
	}
	firstRoom := digString(t, measured, "measurements", 0, "room")
	if firstRoom != "Lab A" {
		t.Fatalf("first measurement room = %q, want Lab A", firstRoom)
	}

	// Corrected relocation succeeds.
	status, moved := api.relocate(deviceID, "Lab B", "Lin", "moved to the new wing")
	if status != http.StatusCreated {
		t.Fatalf("valid relocation status = %d, body = %v", status, moved)
	}
	if room := digString(t, moved, "device", "room"); room != "Lab B" {
		t.Fatalf("device room = %q, want Lab B", room)
	}

	// The first measurement is now review_ready and sealed; reopen is not
	// possible after sealing, so instead verify immutability on the sealed
	// case through a second device relocation.
	status, sealed := api.do(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Wen",
		"summary":  "accepted measurement sealed for review",
	})
	if status != http.StatusCreated {
		t.Fatalf("seal status = %d, body = %v", status, sealed)
	}

	status, _ = api.relocate(deviceID, "Lab C", "Lin", "equipment reorganization")
	if status != http.StatusCreated {
		t.Fatalf("post-seal relocation status = %d", status)
	}

	status, reread := api.do(http.MethodGet, "/cases/"+caseID, nil)
	if status != http.StatusOK {
		t.Fatalf("get sealed case status = %d", status)
	}
	room := digString(t, reread, "measurements", 0, "room")
	if room != "Lab A" {
		t.Fatalf("sealed measurement room after relocation = %q, want Lab A", room)
	}
	state := digString(t, reread, "case", "state")
	if state != "sealed" {
		t.Fatalf("case state = %q, want sealed", state)
	}

	// Sealed cases reject further measurements.
	status, denied := api.do(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed": 25.0, "technician": "Rui",
	})
	if status != http.StatusConflict {
		t.Fatalf("measurement on sealed case status = %d, body = %v", status, denied)
	}
}

// A relocation while a case is open must apply to subsequent measurements,
// proving the move only affects measurements that happen afterwards.
func TestMeasurementAfterRelocationUsesNewRoom(t *testing.T) {
	api := newClient(t)
	deviceID := api.createDevice("MET-4002", "Lab A")
	caseID := api.openCase(deviceID)

	status, first := api.do(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed": 26.5, "technician": "Rui", // out of tolerance
	})
	if status != http.StatusCreated {
		t.Fatalf("rejected measurement status = %d, body = %v", status, first)
	}

	status, _ = api.relocate(deviceID, "Lab B", "Lin", "moved to the new wing")
	if status != http.StatusCreated {
		t.Fatalf("relocation status = %d", status)
	}

	status, reopened := api.do(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor": "Lin", "reason": "re-measure after relocation",
	})
	if status != http.StatusOK {
		t.Fatalf("reopen status = %d, body = %v", status, reopened)
	}

	status, second := api.do(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed": 25.1, "technician": "Rui",
	})
	if status != http.StatusCreated {
		t.Fatalf("second measurement status = %d, body = %v", status, second)
	}
	if room := digString(t, second, "measurements", 0, "room"); room != "Lab A" {
		t.Fatalf("historical measurement room = %q, want Lab A", room)
	}
	if room := digString(t, second, "measurements", 1, "room"); room != "Lab B" {
		t.Fatalf("new measurement room = %q, want Lab B", room)
	}
}
