package server

import (
	"context"
	"net/http"
	"time"
)

func handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !isOnline {
		writeErr(w, http.StatusBadRequest, "offline mode")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := tableDB.Checkpoint(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "checkpoint: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
