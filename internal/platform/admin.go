package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MUYI-luyu/trpc-agent-platform/internal/tenant"
)

// AdminAPI provides HTTP handlers for tenant lifecycle management.
// All endpoints require authentication (not implemented — placeholder for
// API key or OAuth middleware).
type AdminAPI struct {
	manager tenant.Manager
	// onTenantChange is called when a tenant is created/updated/deleted
	// to invalidate Worker caches.
	onTenantChange func(change tenant.ConfigChange)
}

// NewAdminAPI creates a new Admin API handler.
func NewAdminAPI(mgr tenant.Manager, onChange func(tenant.ConfigChange)) *AdminAPI {
	return &AdminAPI{
		manager:        mgr,
		onTenantChange: onChange,
	}
}

// RegisterRoutes registers admin routes on the given mux.
func (a *AdminAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/admin/tenants", a.handleTenants)        // GET (list), POST (create)
	mux.HandleFunc("/admin/tenants/", a.handleTenantByID)    // GET, PUT, DELETE by ID
	mux.HandleFunc("/admin/health", a.handleAdminHealth)
}

// handleTenants handles GET /admin/tenants (list) and POST /admin/tenants (create).
func (a *AdminAPI) handleTenants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listTenants(w, r)
	case http.MethodPost:
		a.createTenant(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTenantByID handles GET/PUT/DELETE /admin/tenants/:id.
func (a *AdminAPI) handleTenantByID(w http.ResponseWriter, r *http.Request) {
	// Extract tenant ID from URL: /admin/tenants/<id>
	id := strings.TrimPrefix(r.URL.Path, "/admin/tenants/")
	if id == "" {
		http.Error(w, "tenant id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.getTenant(w, r, id)
	case http.MethodPut:
		a.updateTenant(w, r, id)
	case http.MethodDelete:
		a.deleteTenant(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *AdminAPI) handleAdminHealth(w http.ResponseWriter, _ *http.Request) {
	tenants, _ := a.manager.List(context.Background())
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"tenant_count": len(tenants),
	})
}

// ─── CRUD ────────────────────────────────────────────────────────────

func (a *AdminAPI) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := a.manager.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if tenants == nil {
		tenants = []*tenant.Tenant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": tenants})
}

func (a *AdminAPI) createTenant(w http.ResponseWriter, r *http.Request) {
	var t tenant.Tenant
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid body: %v", err)})
		return
	}

	if t.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}

	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	if t.Status == "" {
		t.Status = tenant.StatusActive
	}
	if t.DataBackendConfig.Type == "" {
		t.DataBackendConfig.Type = tenant.BackendInMemory
	}

	if err := a.manager.Create(r.Context(), &t); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	log.Printf("admin: created tenant %q", t.ID)
	a.notifyChange(tenant.ConfigChange{
		Type:     tenant.ChangeCreate,
		TenantID: t.ID,
		After:    &t,
	})

	writeJSON(w, http.StatusCreated, map[string]any{"tenant": &t})
}

func (a *AdminAPI) getTenant(w http.ResponseWriter, r *http.Request, id string) {
	t, err := a.manager.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t})
}

func (a *AdminAPI) updateTenant(w http.ResponseWriter, r *http.Request, id string) {
	var updates tenant.Tenant
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid body: %v", err)})
		return
	}

	// Fetch existing, apply updates.
	existing, err := a.manager.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	old := *existing

	// Merge updates (simple top-level merge).
	applyUpdates(existing, &updates)
	existing.UpdatedAt = time.Now()

	if err := a.manager.Update(r.Context(), existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	log.Printf("admin: updated tenant %q", id)
	a.notifyChange(tenant.ConfigChange{
		Type:     tenant.ChangeUpdate,
		TenantID: id,
		Before:   &old,
		After:    existing,
	})

	writeJSON(w, http.StatusOK, map[string]any{"tenant": existing})
}

func (a *AdminAPI) deleteTenant(w http.ResponseWriter, r *http.Request, id string) {
	if err := a.manager.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	log.Printf("admin: deleted tenant %q", id)
	a.notifyChange(tenant.ConfigChange{
		Type:     tenant.ChangeDelete,
		TenantID: id,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *AdminAPI) notifyChange(change tenant.ConfigChange) {
	if a.onTenantChange != nil {
		a.onTenantChange(change)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// applyUpdates merges non-zero fields from updates into existing.
func applyUpdates(existing, updates *tenant.Tenant) {
	if updates.Name != "" {
		existing.Name = updates.Name
	}
	if updates.Status != "" {
		existing.Status = updates.Status
	}
	if updates.ModelConfig.ModelName != "" {
		existing.ModelConfig = updates.ModelConfig
	}
	if updates.ToolPermissions.Mode != "" {
		existing.ToolPermissions = updates.ToolPermissions
	}
	if updates.DataBackendConfig.Type != "" {
		existing.DataBackendConfig = updates.DataBackendConfig
	}
	if updates.AuditPolicy.Level != "" {
		existing.AuditPolicy = updates.AuditPolicy
	}
	if updates.RateLimits.RequestsPerSecond > 0 {
		existing.RateLimits = updates.RateLimits
	}
	if len(updates.IMChannelConfigs) > 0 {
		existing.IMChannelConfigs = updates.IMChannelConfigs
	}
}
