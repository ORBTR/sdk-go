package orbtr

import (
	"bytes"
	"encoding/json"
)

// StringOr is a field that is either a string or an array of T, such as a message's content (its text, or
// its blocks) or an embeddings input (one text, or several). Set exactly one of OfString and OfArray; the
// generated params use it for each field the contract declares as a string or an array.
type StringOr[T any] struct {
	OfString *string
	OfArray  []T
}

// StringOf is the string form.
func StringOf[T any](s string) StringOr[T] { return StringOr[T]{OfString: &s} }

// ArrayOf is the array form.
func ArrayOf[T any](v ...T) StringOr[T] {
	if v == nil {
		v = []T{}
	}
	return StringOr[T]{OfArray: v}
}

// MarshalJSON writes the string when OfString is set, and otherwise the array.
func (u StringOr[T]) MarshalJSON() ([]byte, error) {
	if u.OfString != nil {
		return json.Marshal(*u.OfString)
	}
	if u.OfArray == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(u.OfArray)
}

// UnmarshalJSON reads either form.
func (u *StringOr[T]) UnmarshalJSON(data []byte) error {
	*u = StringOr[T]{}
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return err
		}
		u.OfString = &s
		return nil
	}
	return json.Unmarshal(data, &u.OfArray)
}
