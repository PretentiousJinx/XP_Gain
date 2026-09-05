package firebase

import (
	"strconv"
	"strings"
	"time"
)

// Value is a Firestore typed value. Firestore's REST API does not accept bare
// JSON: every field must declare its type, and integers in particular must be
// sent as *strings* so that int64 values survive JSON's float64 number type.
type Value map[string]any

// EncodeFields converts a SQLite row into a Firestore field map.
//
// The column name decides the encoding. Anything ending in `_at` is treated as
// an instant and stored as a real timestamp rather than text, so Firestore can
// range-query it; `local_date` stays a string because it is a calendar date in
// the user's zone, not a point in time, and coercing it to a timestamp would
// silently re-anchor it to UTC midnight.
func EncodeFields(row map[string]any) map[string]Value {
	out := make(map[string]Value, len(row))
	for col, v := range row {
		out[col] = encodeColumn(col, v)
	}
	return out
}

func encodeColumn(col string, v any) Value {
	if v == nil {
		return Value{"nullValue": nil}
	}
	if strings.HasSuffix(col, "_at") {
		if s, ok := v.(string); ok {
			if ts, err := parseTimestamp(s); err == nil {
				return Value{"timestampValue": ts.UTC().Format(time.RFC3339Nano)}
			}
		}
	}
	return EncodeValue(v)
}

// EncodeValue converts a single Go value to its Firestore representation.
func EncodeValue(v any) Value {
	switch t := v.(type) {
	case nil:
		return Value{"nullValue": nil}
	case bool:
		return Value{"booleanValue": t}
	case int:
		return Value{"integerValue": strconv.FormatInt(int64(t), 10)}
	case int32:
		return Value{"integerValue": strconv.FormatInt(int64(t), 10)}
	case int64:
		return Value{"integerValue": strconv.FormatInt(t, 10)}
	case float32:
		return Value{"doubleValue": float64(t)}
	case float64:
		// SQLite hands back whole numbers from INTEGER columns as float64 on
		// some driver paths. Preserve integer-ness so a level of 7 does not
		// arrive in Firestore as 7.0 and break a strict client decoder.
		if t == float64(int64(t)) {
			return Value{"integerValue": strconv.FormatInt(int64(t), 10)}
		}
		return Value{"doubleValue": t}
	case string:
		return Value{"stringValue": t}
	case []byte:
		return Value{"stringValue": string(t)}
	case time.Time:
		return Value{"timestampValue": t.UTC().Format(time.RFC3339Nano)}
	default:
		return Value{"stringValue": toString(t)}
	}
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, errNotATimestamp
}

type constErr string

func (e constErr) Error() string { return string(e) }

const errNotATimestamp = constErr("not a timestamp")

func toString(v any) string {
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}
