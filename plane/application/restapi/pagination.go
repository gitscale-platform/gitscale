package restapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/application/repositories"
)

// errInvalidCursor is returned when DecodeCursor cannot parse the input.
var errInvalidCursor = errors.New("restapi: invalid cursor")

// EncodeCursor renders c as a URL-safe base64 of its JSON form. The empty
// cursor renders to the empty string so callers can omit the parameter on
// the first page.
func EncodeCursor(c repositories.Cursor) string {
	if c.IsZero() {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		// json.Marshal of a value-typed struct of UUID + Time cannot fail
		// in practice; collapse the error to an empty cursor rather than
		// panicking on the request path.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor parses an opaque cursor string. The empty input is the
// "start of listing" zero value with no error.
func DecodeCursor(s string) (repositories.Cursor, error) {
	if s == "" {
		return repositories.Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return repositories.Cursor{}, errInvalidCursor
	}
	var c repositories.Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return repositories.Cursor{}, errInvalidCursor
	}
	return c, nil
}
