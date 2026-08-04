package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ghrouter/internal/types"
)

type controlPlaneValidationError struct {
	cause error
}

func (e *controlPlaneValidationError) Error() string { return e.cause.Error() }

func (e *controlPlaneValidationError) Unwrap() error { return e.cause }

type controlPlanePayload struct {
	Connections []types.Connection `json:"connections"`
	Pools       []types.Pool       `json:"pools"`
	Combos      []types.Combo      `json:"combos"`
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func (s *Server) handleControlPlane(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid ghrouter access token required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeControlPlane(w)
	case http.MethodPut:
		if !s.adminAuthorized(r) {
			writeError(w, http.StatusForbidden, "admin_required", "administrative token required")
			return
		}
		var payload controlPlanePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_control_plane", err.Error())
			return
		}
		if err := s.reloadControlPlane(payload); err != nil {
			var validationErr *controlPlaneValidationError
			if errors.As(err, &validationErr) || strings.HasPrefix(err.Error(), "connection") || strings.HasPrefix(err.Error(), "pool") || strings.HasPrefix(err.Error(), "combo") || strings.Contains(err.Error(), "name") {
				writeError(w, http.StatusBadRequest, "invalid_control_plane", err.Error())
				return
			}
			writeError(w, http.StatusConflict, "control_plane_reload_failed", err.Error())
			return
		}
		s.recordAudit("control_plane_replaced", "admin", map[string]any{
			"connections": len(payload.Connections), "pools": len(payload.Pools), "combos": len(payload.Combos),
		})
		s.writeControlPlane(w)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "control plane supports GET and PUT")
	}
}

func (s *Server) handleControlPlaneResource(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid ghrouter access token required")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/control-plane/"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeError(w, http.StatusNotFound, "resource_not_found", "expected /v1/control-plane/{connection|pool|combo}/{name}")
		return
	}
	kind, name := strings.ToLower(parts[0]), parts[1]
	payload := s.controlPlanePayload()
	index := resourceIndex(kind, name, payload)
	if r.Method == http.MethodGet {
		if index < 0 {
			writeError(w, http.StatusNotFound, "resource_not_found", "control-plane resource not found")
			return
		}
		writeControlPlaneResource(w, kind, resourceAt(kind, index, payload))
		return
	}
	if !s.adminAuthorized(r) {
		writeError(w, http.StatusForbidden, "admin_required", "administrative token required")
		return
	}
	switch r.Method {
	case http.MethodPut:
		if err := decodeControlPlaneResource(r, kind, name, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_control_plane", err.Error())
			return
		}
		if err := validateControlPlane(payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_control_plane", err.Error())
			return
		}
	case http.MethodDelete:
		if !removeControlPlaneResource(kind, index, &payload) {
			writeError(w, http.StatusNotFound, "resource_not_found", "control-plane resource not found")
			return
		}
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "resource supports GET, PUT and DELETE")
		return
	}
	if err := s.reloadControlPlane(payload); err != nil {
		var validationErr *controlPlaneValidationError
		if errors.As(err, &validationErr) {
			writeError(w, http.StatusBadRequest, "invalid_control_plane", err.Error())
			return
		}
		writeError(w, http.StatusConflict, "control_plane_reload_failed", err.Error())
		return
	}
	s.recordAudit("control_plane_resource_changed", "admin", map[string]any{
		"kind": kind, "name": name, "operation": strings.ToLower(r.Method),
	})
	updated := s.controlPlanePayload()
	updatedIndex := resourceIndex(kind, name, updated)
	if updatedIndex < 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeControlPlaneResource(w, kind, resourceAt(kind, updatedIndex, updated))
}

func (s *Server) reloadControlPlane(payload controlPlanePayload) error {
	if err := validateControlPlane(payload); err != nil {
		return &controlPlaneValidationError{cause: err}
	}
	s.mu.RLock()
	next := *s.cfg
	next.Connections = append([]types.Connection(nil), payload.Connections...)
	next.Pools = append([]types.Pool(nil), payload.Pools...)
	next.Combos = append([]types.Combo(nil), payload.Combos...)
	s.mu.RUnlock()
	if err := validateControlPlaneReferences(s, payload, &next); err != nil {
		return &controlPlaneValidationError{cause: err}
	}
	return s.ReloadConfig(&next)
}

func (s *Server) controlPlanePayload() controlPlanePayload {
	connections, pools, combos := s.controlPlaneSummaries()
	return controlPlanePayload{Connections: summariesToConnections(connections), Pools: summariesToPools(pools), Combos: summariesToCombos(combos)}
}

func resourceIndex(kind, name string, payload controlPlanePayload) int {
	switch kind {
	case "connection", "connections":
		for i := range payload.Connections {
			if payload.Connections[i].Name == name {
				return i
			}
		}
	case "pool", "pools":
		for i := range payload.Pools {
			if payload.Pools[i].Name == name {
				return i
			}
		}
	case "combo", "combos":
		for i := range payload.Combos {
			if payload.Combos[i].Name == name {
				return i
			}
		}
	}
	return -1
}

func resourceAt(kind string, index int, payload controlPlanePayload) any {
	switch kind {
	case "connection", "connections":
		return payload.Connections[index]
	case "pool", "pools":
		return payload.Pools[index]
	default:
		return payload.Combos[index]
	}
}

func writeControlPlaneResource(w http.ResponseWriter, kind string, resource any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resource)
}

func decodeControlPlaneResource(r *http.Request, kind, name string, payload *controlPlanePayload) error {
	decoder := json.NewDecoder(r.Body)
	switch kind {
	case "connection", "connections":
		var resource types.Connection
		if err := decoder.Decode(&resource); err != nil {
			return err
		}
		if resource.Name == "" {
			resource.Name = name
		}
		if resource.Name != name {
			return fmt.Errorf("resource name %q does not match path", resource.Name)
		}
		index := resourceIndex(kind, name, *payload)
		if index < 0 {
			payload.Connections = append(payload.Connections, resource)
		} else {
			payload.Connections[index] = resource
		}
	case "pool", "pools":
		var resource types.Pool
		if err := decoder.Decode(&resource); err != nil {
			return err
		}
		if resource.Name == "" {
			resource.Name = name
		}
		if resource.Name != name {
			return fmt.Errorf("resource name %q does not match path", resource.Name)
		}
		index := resourceIndex(kind, name, *payload)
		if index < 0 {
			payload.Pools = append(payload.Pools, resource)
		} else {
			payload.Pools[index] = resource
		}
	case "combo", "combos":
		var resource types.Combo
		if err := decoder.Decode(&resource); err != nil {
			return err
		}
		if resource.Name == "" {
			resource.Name = name
		}
		if resource.Name != name {
			return fmt.Errorf("resource name %q does not match path", resource.Name)
		}
		index := resourceIndex(kind, name, *payload)
		if index < 0 {
			payload.Combos = append(payload.Combos, resource)
		} else {
			payload.Combos[index] = resource
		}
	default:
		return fmt.Errorf("unsupported control-plane resource %q", kind)
	}
	return nil
}

func removeControlPlaneResource(kind string, index int, payload *controlPlanePayload) bool {
	if index < 0 {
		return false
	}
	switch kind {
	case "connection", "connections":
		payload.Connections = append(payload.Connections[:index], payload.Connections[index+1:]...)
	case "pool", "pools":
		payload.Pools = append(payload.Pools[:index], payload.Pools[index+1:]...)
	case "combo", "combos":
		payload.Combos = append(payload.Combos[:index], payload.Combos[index+1:]...)
	default:
		return false
	}
	return true
}

func (s *Server) writeControlPlane(w http.ResponseWriter) {
	connections, pools, combos := s.controlPlaneSummaries()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(controlPlanePayload{Connections: summariesToConnections(connections), Pools: summariesToPools(pools), Combos: summariesToCombos(combos)})
}

func summariesToConnections(values []ConnectionSummary) []types.Connection {
	out := make([]types.Connection, 0, len(values))
	for _, value := range values {
		out = append(out, types.Connection{Name: value.Name, Provider: value.Provider, Model: value.Model, Enabled: value.Enabled, Metadata: cloneStringMap(value.Metadata)})
	}
	return out
}

func summariesToPools(values []PoolSummary) []types.Pool {
	out := make([]types.Pool, 0, len(values))
	for _, value := range values {
		out = append(out, types.Pool{Name: value.Name, Members: append([]string(nil), value.Members...), Strategy: value.Strategy, Enabled: value.Enabled})
	}
	return out
}

func summariesToCombos(values []ComboSummary) []types.Combo {
	out := make([]types.Combo, 0, len(values))
	for _, value := range values {
		out = append(out, types.Combo{Name: value.Name, Members: append([]string(nil), value.Members...), Strategy: value.Strategy, Judge: value.Judge, Enabled: value.Enabled})
	}
	return out
}

func validateControlPlane(payload controlPlanePayload) error {
	names := make(map[string]string)
	for _, connection := range payload.Connections {
		if err := validateResourceName(connection.Name, "connection"); err != nil {
			return err
		}
		if connection.Provider == "" || connection.Model == "" {
			return fmt.Errorf("connection %q requires provider and model", connection.Name)
		}
		if err := reserveResourceName(names, connection.Name, "connection"); err != nil {
			return err
		}
	}
	for _, pool := range payload.Pools {
		if err := validateResourceName(pool.Name, "pool"); err != nil {
			return err
		}
		if len(pool.Members) == 0 {
			return fmt.Errorf("pool %q requires at least one member", pool.Name)
		}
		if err := validateStrategy(pool.Strategy); err != nil {
			return fmt.Errorf("pool %q: %w", pool.Name, err)
		}
		if err := reserveResourceName(names, pool.Name, "pool"); err != nil {
			return err
		}
	}
	for _, combo := range payload.Combos {
		if err := validateResourceName(combo.Name, "combo"); err != nil {
			return err
		}
		if len(combo.Members) == 0 {
			return fmt.Errorf("combo %q requires at least one member", combo.Name)
		}
		if err := validateStrategy(combo.Strategy); err != nil {
			return fmt.Errorf("combo %q: %w", combo.Name, err)
		}
		if err := reserveResourceName(names, combo.Name, "combo"); err != nil {
			return err
		}
	}
	return nil
}

func validateControlPlaneReferences(s *Server, payload controlPlanePayload, cfg *types.Config) error {
	if cfg == nil {
		return fmt.Errorf("control-plane configuration is missing")
	}
	known := make(map[string]struct{}, len(cfg.ModelLists)+len(payload.Connections)+len(payload.Pools)+len(payload.Combos))
	for _, list := range cfg.ModelLists {
		known[list.Name] = struct{}{}
	}
	for _, connection := range payload.Connections {
		known[connection.Name] = struct{}{}
	}
	for _, pool := range payload.Pools {
		known[pool.Name] = struct{}{}
	}
	for _, combo := range payload.Combos {
		known[combo.Name] = struct{}{}
	}

	resolve := func(reference string) bool {
		reference = strings.TrimSpace(reference)
		if reference == "" {
			return false
		}
		if _, ok := known[reference]; ok {
			return true
		}
		for _, provider := range cfg.Providers {
			if provider == nil || !provider.Enabled {
				continue
			}
			if reference == provider.Name {
				return false
			}
			if strings.HasPrefix(reference, provider.Name+"/") && s.resolveModel(provider, strings.TrimPrefix(reference, provider.Name+"/")) != "" {
				return true
			}
			if prefix := prefixFor(provider.Type); prefix != "" && strings.HasPrefix(reference, prefix) && s.resolveModel(provider, strings.TrimPrefix(reference, prefix)) != "" {
				return true
			}
			if s.resolveModel(provider, reference) != "" {
				return true
			}
		}
		return false
	}

	for _, connection := range payload.Connections {
		if !resolve(connection.Provider + "/" + connection.Model) {
			return fmt.Errorf("connection %q references unknown provider/model %q/%q", connection.Name, connection.Provider, connection.Model)
		}
	}
	for _, pool := range payload.Pools {
		for _, member := range pool.Members {
			if !resolve(member) {
				return fmt.Errorf("pool %q references unknown member %q", pool.Name, member)
			}
		}
	}
	for _, combo := range payload.Combos {
		for _, member := range combo.Members {
			if !resolve(member) {
				return fmt.Errorf("combo %q references unknown member %q", combo.Name, member)
			}
		}
		if combo.Judge != "" && !resolve(combo.Judge) {
			return fmt.Errorf("combo %q references unknown judge %q", combo.Name, combo.Judge)
		}
	}
	if err := validateControlPlaneCycles(payload); err != nil {
		return err
	}
	return nil
}

func validateControlPlaneCycles(payload controlPlanePayload) error {
	edges := make(map[string][]string, len(payload.Pools)+len(payload.Combos))
	for _, pool := range payload.Pools {
		edges[pool.Name] = append([]string(nil), pool.Members...)
	}
	for _, combo := range payload.Combos {
		edges[combo.Name] = append([]string(nil), combo.Members...)
		if combo.Judge != "" {
			edges[combo.Name] = append(edges[combo.Name], combo.Judge)
		}
	}
	state := make(map[string]uint8, len(edges))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("control-plane resource cycle detected at %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, reference := range edges[name] {
			if _, ok := edges[reference]; ok {
				if err := visit(reference); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		return nil
	}
	for name := range edges {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func validateResourceName(name, kind string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s name is required", kind)
	}
	if strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("%s name contains a newline", kind)
	}
	return nil
}

func reserveResourceName(names map[string]string, name, kind string) error {
	if previous, ok := names[name]; ok {
		return fmt.Errorf("resource name %q is already used by %s", name, previous)
	}
	names[name] = kind
	return nil
}

func validateStrategy(strategy string) error {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "", "score", "round-robin", "fallback", "fusion", "graph", "sticky", "auto":
		return nil
	default:
		return fmt.Errorf("unsupported strategy %q", strategy)
	}
}
