package domain

import (
	"fmt"
	"strings"
	"time"
)

type DeviceStatus string

const (
	DeviceActive  DeviceStatus = "active"
	DeviceRetired DeviceStatus = "retired"
)

type Device struct {
	ID         string       `json:"id"`
	AssetTag   string       `json:"asset_tag"`
	Model      string       `json:"model"`
	Room       string       `json:"room"`
	Steward    string       `json:"steward"`
	Status     DeviceStatus `json:"status"`
	EnrolledAt time.Time    `json:"enrolled_at"`
	RetiredAt  *time.Time   `json:"retired_at,omitempty"`
}

func NewDevice(id, assetTag, model, room, steward string, at time.Time) (Device, error) {
	device := Device{
		ID:         strings.TrimSpace(id),
		AssetTag:   NormalizeAssetTag(assetTag),
		Model:      strings.TrimSpace(model),
		Room:       strings.TrimSpace(room),
		Steward:    strings.TrimSpace(steward),
		Status:     DeviceActive,
		EnrolledAt: at.UTC(),
	}
	if err := device.Validate(); err != nil {
		return Device{}, err
	}
	return device, nil
}

func NormalizeAssetTag(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func (d Device) Validate() error {
	if d.ID == "" || d.AssetTag == "" || d.Model == "" || d.Room == "" || d.Steward == "" {
		return fmt.Errorf("%w: device id, asset tag, model, room, and steward are required", ErrInvalidInput)
	}
	if len(d.AssetTag) > 64 {
		return fmt.Errorf("%w: asset tag is too long", ErrInvalidInput)
	}
	if d.EnrolledAt.IsZero() {
		return fmt.Errorf("%w: enrollment time is required", ErrInvalidInput)
	}
	switch d.Status {
	case DeviceActive, DeviceRetired:
		return nil
	default:
		return fmt.Errorf("%w: unsupported device state %q", ErrInvalidInput, d.Status)
	}
}

func (d Device) IsActive() bool {
	return d.Status == DeviceActive
}

func (d *Device) Retire(at time.Time) error {
	if d.Status != DeviceActive {
		return fmt.Errorf("%w: only an active device can be retired", ErrStateConflict)
	}
	stamp := at.UTC()
	d.Status = DeviceRetired
	d.RetiredAt = &stamp
	return nil
}

func (d *Device) Relocate(room string) error {
	if d.Status != DeviceActive {
		return fmt.Errorf("%w: only an active device can be relocated", ErrStateConflict)
	}
	d.Room = strings.TrimSpace(room)
	return nil
}

type DeviceDeployment struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id"`
	Room       string    `json:"room"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
}

func NewDeviceDeployment(id, deviceID, room, actor, reason string, at time.Time) (DeviceDeployment, error) {
	item := DeviceDeployment{
		ID:         strings.TrimSpace(id),
		DeviceID:   strings.TrimSpace(deviceID),
		Room:       strings.TrimSpace(room),
		Actor:      strings.TrimSpace(actor),
		Reason:     strings.TrimSpace(reason),
		RecordedAt: at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return DeviceDeployment{}, err
	}
	return item, nil
}

func (d DeviceDeployment) Validate() error {
	if d.ID == "" || d.DeviceID == "" || d.Room == "" || d.Actor == "" || d.Reason == "" {
		return fmt.Errorf("%w: deployment id, device id, room, actor, and reason are required", ErrInvalidInput)
	}
	if len(d.Room) > 64 {
		return fmt.Errorf("%w: room is too long", ErrInvalidInput)
	}
	if d.RecordedAt.IsZero() {
		return fmt.Errorf("%w: deployment time is required", ErrInvalidInput)
	}
	return nil
}
