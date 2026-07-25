package base

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sort"
	"time"
)

type MutationRecord struct {
	TS     time.Time                  `json:"ts"`
	Op     string                     `json:"op"`
	Table  string                     `json:"table,omitempty"`
	Key    string                     `json:"key,omitempty"`
	Value  json.RawMessage            `json:"value,omitempty"`
	Fields map[string]json.RawMessage `json:"fields,omitempty"`
	Label  string                     `json:"label,omitempty"`
	ID     string                     `json:"id,omitempty"`
	From   string                     `json:"from,omitempty"`
	To     string                     `json:"to,omitempty"`
}

func MarshalJSONL(recs []MutationRecord) ([]byte, error) {
	var buf bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func UnmarshalJSONL(data []byte) ([]MutationRecord, error) {
	var recs []MutationRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r MutationRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		recs = append(recs, r)
	}
	return recs, sc.Err()
}

// SortByTS sorts mutation records in ascending timestamp order in place.
func SortByTS(recs []MutationRecord) {
	sort.Slice(recs, func(i, j int) bool { return recs[i].TS.Before(recs[j].TS) })
}

// JSONMerge merges fields into existing (a JSON object), returning the merged object.
func JSONMerge(existing json.RawMessage, fields map[string]json.RawMessage) (json.RawMessage, error) {
	base := map[string]json.RawMessage{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &base); err != nil {
			return nil, err
		}
	}
	for k, v := range fields {
		base[k] = v
	}
	return json.Marshal(base)
}
