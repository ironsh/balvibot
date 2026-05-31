package approvalserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ironsh/balvibot/tools/api/internal/approval"
	"github.com/ironsh/balvibot/tools/api/internal/store"
)

// restHandlers serves the balvi-approve CLI: list pending actions, fetch one
// (with its signing payload), and approve one (verify SSH signature + dispatch).
type restHandlers struct {
	st       *store.Store
	registry *approval.Registry
	logger   *slog.Logger
}

// listActions handles GET /actions?status=pending (default pending).
func (h *restHandlers) listActions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = store.ApprovalPending
	}
	actions, err := h.st.ListActionsByStatus(r.Context(), status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if actions == nil {
		actions = []store.ApprovalAction{}
	}
	writeJSON(w, http.StatusOK, ListActionsResponse{Actions: actions})
}

// getAction handles GET /actions/{id}, returning the action plus the exact
// base64 payload the operator must sign.
func (h *restHandlers) getAction(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	a, err := h.st.GetAction(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "action not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	payload := approval.SigningPayload(a.ID, a.Action, rawOrEmpty(a.Args))
	writeJSON(w, http.StatusOK, ActionView{
		ApprovalAction:    *a,
		SigningPayloadB64: base64.StdEncoding.EncodeToString(payload),
	})
}

// approveAction handles POST /actions/{id}/approve: verify the operator's SSH
// signature over the action's signing payload, then dispatch the action.
func (h *restHandlers) approveAction(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req ApproveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Signature == "" {
		writeError(w, http.StatusBadRequest, "email and signature are required")
		return
	}

	ctx := r.Context()
	a, err := h.st.GetAction(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "action not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.Status != store.ApprovalPending {
		writeError(w, http.StatusConflict, "action is not pending (status="+a.Status+")")
		return
	}

	user, err := h.st.GetApprovalUser(ctx, req.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, "no authorized key for "+req.Email)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	payload := approval.SigningPayload(a.ID, a.Action, rawOrEmpty(a.Args))
	if err := approval.Verify(user.PublicKey, payload, req.Signature); err != nil {
		h.logger.Warn("approval signature rejected", "id", id, "email", req.Email, "err", err)
		writeError(w, http.StatusForbidden, "signature verification failed")
		return
	}

	// Signature is good. Dispatch the action; record the outcome.
	if derr := h.registry.Dispatch(ctx, a.Action, rawOrEmpty(a.Args)); derr != nil {
		if ferr := h.st.MarkActionFailed(ctx, id, req.Email, derr.Error()); ferr != nil {
			h.logger.Error("mark failed", "id", id, "err", ferr)
		}
		h.logger.Error("action dispatch failed", "id", id, "action", a.Action, "err", derr)
		writeError(w, http.StatusInternalServerError, "action failed: "+derr.Error())
		return
	}
	if err := h.st.MarkActionExecuted(ctx, id, req.Email); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logger.Info("action approved and executed", "id", id, "action", a.Action, "approved_by", req.Email)
	writeJSON(w, http.StatusOK, ApproveResponse{ApprovalID: id, Status: store.ApprovalExecuted})
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid action id")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
