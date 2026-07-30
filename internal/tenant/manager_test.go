package tenant

import (
	"context"
	"testing"
)

func TestInMemoryManager_CreateAndGet(t *testing.T) {
	m := NewInMemoryManager()
	ctx := context.Background()

	t1 := &Tenant{
		ID:   "tenant_001",
		Name: "Test Tenant",
		DataBackendConfig: DataBackendConfig{
			Type: "inmemory",
		},
	}

	if err := m.Create(ctx, t1); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := m.Get(ctx, "tenant_001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Test Tenant" {
		t.Errorf("Name = %q, want %q", got.Name, "Test Tenant")
	}
	if got.Status != StatusActive {
		t.Errorf("Status = %q, want %q", got.Status, StatusActive)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestInMemoryManager_CreateDuplicate(t *testing.T) {
	m := NewInMemoryManager()
	ctx := context.Background()

	t1 := &Tenant{ID: "dup"}
	_ = m.Create(ctx, t1)

	if err := m.Create(ctx, t1); err == nil {
		t.Error("expected error on duplicate Create")
	}
}

func TestInMemoryManager_GetNotFound(t *testing.T) {
	m := NewInMemoryManager()
	ctx := context.Background()

	_, err := m.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown tenant")
	}
}

func TestInMemoryManager_Update(t *testing.T) {
	m := NewInMemoryManager()
	ctx := context.Background()

	_ = m.Create(ctx, &Tenant{ID: "t1", Name: "original"})

	updated := &Tenant{ID: "t1", Name: "updated"}
	if err := m.Update(ctx, updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := m.Get(ctx, "t1")
	if got.Name != "updated" {
		t.Errorf("Name = %q, want %q", got.Name, "updated")
	}
}

func TestInMemoryManager_Delete(t *testing.T) {
	m := NewInMemoryManager()
	ctx := context.Background()

	_ = m.Create(ctx, &Tenant{ID: "t1"})

	if err := m.Delete(ctx, "t1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := m.Get(ctx, "t1")
	if err == nil {
		t.Error("expected error after Delete")
	}
}

func TestInMemoryManager_List(t *testing.T) {
	m := NewInMemoryManager()
	ctx := context.Background()

	_ = m.Create(ctx, &Tenant{ID: "t1"})
	_ = m.Create(ctx, &Tenant{ID: "t2"})

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len(List) = %d, want 2", len(list))
	}
}

func TestInMemoryManager_Watch(t *testing.T) {
	m := NewInMemoryManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := m.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	_ = m.Create(ctx, &Tenant{ID: "t1"})

	select {
	case change := <-ch:
		if change.Type != ChangeCreate || change.TenantID != "t1" {
			t.Errorf("unexpected change: %+v", change)
		}
	default:
		t.Error("expected a change event on Create")
	}
}

func TestTenant_IsActive(t *testing.T) {
	active := &Tenant{Status: StatusActive}
	if !active.IsActive() {
		t.Error("active tenant should be active")
	}
	suspended := &Tenant{Status: StatusSuspended}
	if suspended.IsActive() {
		t.Error("suspended tenant should not be active")
	}
}
