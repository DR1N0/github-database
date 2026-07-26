package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/DR1N0/github-database/ghdb"
)

func handleTable(w http.ResponseWriter, r *http.Request, parts []string) {
	// parts: ["user_info"] or ["user_info", key]
	if len(parts) < 1 || parts[0] != "user_info" {
		http.NotFound(w, r)
		return
	}
	tbl := tableDB.Table("user_info")
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, tbl.All())
		return
	}
	key := parts[1]
	switch r.Method {
	case http.MethodGet:
		val, ok := tbl.Get(key)
		if !ok {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(val)
	case http.MethodPut:
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := tbl.Set(key, body); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ghdb.ErrKeyMismatch) || errors.Is(err, ghdb.ErrRequiredMissing) {
				status = http.StatusBadRequest
			}
			writeErr(w, status, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := tbl.Delete(key); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
