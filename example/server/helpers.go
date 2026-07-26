package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		logger.Printf("dotenv: %v (continuing)", err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key, val := line[:idx], line[idx+1:]
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	if err := sc.Err(); err != nil {
		logger.Printf("dotenv: read error: %v", err)
	}
}
