package repository

import (
	"context"
	"testing"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
)

// CurrentRoom must resolve to the room of the most recent deployment. It used
// to return the earliest deployment, so measurements taken after a relocation
// were stamped with the original room.
func TestCurrentRoomFollowsLatestDeployment(t *testing.T) {
	repo := New()
	ctx := context.Background()
	enrolledAt := time.Unix(1700000000, 0).UTC()

	err := repo.Mutate(ctx, func(m *Mutation) error {
		device, err := domain.NewDevice(m.NextID("device"), " met-2001 ", "Bench Meter", "Lab A", "Lin", enrolledAt)
		if err != nil {
			return err
		}
		if err := m.PutDevice(device); err != nil {
			return err
		}
		initial, err := domain.NewDeviceDeployment(m.NextID("deployment"), device.ID, "Lab A", "Lin", "device enrolled at its initial room", enrolledAt)
		if err != nil {
			return err
		}
		if err := m.AppendDeployment(initial); err != nil {
			return err
		}
		if got := m.CurrentRoom(device.ID); got != "Lab A" {
			t.Fatalf("current room after enrollment = %q, want Lab A", got)
		}
		moved, err := domain.NewDeviceDeployment(m.NextID("deployment"), device.ID, "Lab B", "Lin", "moved to the new wing", enrolledAt.Add(time.Hour))
		if err != nil {
			return err
		}
		if err := m.AppendDeployment(moved); err != nil {
			return err
		}
		if got := m.CurrentRoom(device.ID); got != "Lab B" {
			t.Fatalf("current room after relocation = %q, want Lab B", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
}

// A mutation that returns an error must leave the committed state untouched:
// copy-on-write rollback means a rejected relocation cannot partially land.
func TestMutateRollsBackOnError(t *testing.T) {
	repo := New()
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()

	if err := repo.Mutate(ctx, func(m *Mutation) error {
		device, err := domain.NewDevice(m.NextID("device"), "MET-2002", "Bench Meter", "Lab A", "Lin", at)
		if err != nil {
			return err
		}
		return m.PutDevice(device)
	}); err != nil {
		t.Fatalf("seed mutate: %v", err)
	}

	boom := context.Canceled
	_ = repo.Mutate(ctx, func(m *Mutation) error {
		device, _ := m.Device("device-0001")
		if err := device.Relocate("Lab B"); err != nil {
			return err
		}
		if err := m.UpdateDevice(device); err != nil {
			return err
		}
		return boom
	})

	if err := repo.Read(ctx, func(snapshot Snapshot) error {
		device, ok := snapshot.Device("device-0001")
		if !ok {
			t.Fatal("device disappeared after rolled-back mutation")
		}
		if device.Room != "Lab A" {
			t.Fatalf("room after failed mutation = %q, want Lab A", device.Room)
		}
		if len(snapshot.Deployments(device.ID)) != 0 {
			t.Fatal("failed mutation appended a deployment")
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}
