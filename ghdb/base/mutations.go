package base

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// JSONLLineError identifies a malformed JSONL source line.
// Line is one-based and includes blank lines.
type JSONLLineError struct {
	Line int
	Err  error

	oversized bool
}

func (e *JSONLLineError) Error() string {
	return fmt.Sprintf("ghdb: JSONL line %d: %v", e.Line, e.Err)
}

func (e *JSONLLineError) Unwrap() error { return e.Err }

func jsonlWarningCategory(e *JSONLLineError) string {
	if e.oversized {
		return "exceeds 32 MiB individual-line limit"
	}
	return "malformed JSONL record"
}

type decodedMutationRecord struct {
	record MutationRecord
	line   int
}

// decodeJSONL parses records line-by-line. Unlike UnmarshalJSONL, it retains
// source-line information and reports malformed lines without stopping.
func decodeJSONL(data []byte) ([]decodedMutationRecord, []*JSONLLineError) {
	var recs []decodedMutationRecord
	var errs []*JSONLLineError
	for lineNumber, remaining := 1, data; len(remaining) > 0; lineNumber++ {
		line := remaining
		if newline := bytes.IndexByte(remaining, '\n'); newline >= 0 {
			line = remaining[:newline]
			remaining = remaining[newline+1:]
		} else {
			remaining = nil
		}

		if len(line) > MaxSingleMutationBytes {
			errs = append(errs, &JSONLLineError{
				Line:      lineNumber,
				Err:       fmt.Errorf("line exceeds %d byte limit", MaxSingleMutationBytes),
				oversized: true,
			})
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r MutationRecord
		if err := json.Unmarshal(line, &r); err != nil {
			errs = append(errs, &JSONLLineError{Line: lineNumber, Err: err})
			continue
		}
		recs = append(recs, decodedMutationRecord{record: r, line: lineNumber})
	}
	return recs, errs
}

// UnmarshalJSONL strictly decodes JSONL. It returns the first malformed or
// oversized line rather than omitting it. Valid lines up to 32 MiB are accepted.
func UnmarshalJSONL(data []byte) ([]MutationRecord, error) {
	decoded, errs := decodeJSONL(data)
	if len(errs) > 0 {
		return nil, errs[0]
	}
	var recs []MutationRecord
	for _, decoded := range decoded {
		recs = append(recs, decoded.record)
	}
	return recs, nil
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
