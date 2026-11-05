// Package assertionid implements the assertion identity recipe (id scheme 2)
// byte for byte. The SDKs compute ids with the same recipe; the shared golden
// vectors in protocol-tests/ids/assertion-ids.json pin both sides.
//
// Recipe:
//
//	canonical_spec = RFC 8785 (JCS) serialization of the assertion spec
//	input          = JCS array [scope_id, assertion_type, spec, occurrence_index]
//	id             = "a2_" + first 32 lowercase hex chars of SHA-256(UTF-8(input))
package assertionid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Prefix marks an id as scheme 2.
const Prefix = "a2_"

const hexChars = 32

// Canonicalize returns the RFC 8785 serialization of a JSON document.
func Canonicalize(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("assertionid: input is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("assertionid: trailing data after JSON value")
	}
	var out bytes.Buffer
	if err := write(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Input returns the canonical bytes that are hashed to form an id. An empty
// spec is the empty object.
func Input(scopeID, assertionType string, spec []byte, occurrence int) ([]byte, error) {
	if occurrence < 0 {
		return nil, fmt.Errorf("assertionid: occurrence index %d is negative", occurrence)
	}
	if len(bytes.TrimSpace(spec)) == 0 {
		spec = []byte("{}")
	}
	canon, err := Canonicalize(spec)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteByte('[')
	writeString(&out, scopeID)
	out.WriteByte(',')
	writeString(&out, assertionType)
	out.WriteByte(',')
	out.Write(canon)
	out.WriteByte(',')
	out.WriteString(strconv.Itoa(occurrence))
	out.WriteByte(']')
	return out.Bytes(), nil
}

// ID computes the scheme-2 assertion id.
func ID(scopeID, assertionType string, spec []byte, occurrence int) (string, error) {
	in, err := Input(scopeID, assertionType, spec, occurrence)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(in)
	return Prefix + hex.EncodeToString(sum[:])[:hexChars], nil
}

// node kinds produced by decodeValue.
type object struct {
	keys []string
	vals map[string]any
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("assertionid: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &object{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("assertionid: %w", err)
				}
				key := kt.(string)
				if _, dup := obj.vals[key]; dup {
					return nil, fmt.Errorf("assertionid: duplicate object key %q", key)
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj.keys = append(obj.keys, key)
				obj.vals[key] = val
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("assertionid: %w", err)
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("assertionid: %w", err)
			}
			return arr, nil
		}
		return nil, fmt.Errorf("assertionid: unexpected delimiter %q", t)
	default:
		return tok, nil
	}
}

func write(out *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(t))
	case string:
		writeString(out, t)
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("assertionid: number %q is not representable as an IEEE 754 double", string(t))
		}
		out.WriteString(formatNumber(f))
	case []any:
		out.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := write(out, e); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case *object:
		keys := append([]string(nil), t.keys...)
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeString(out, k)
			out.WriteByte(':')
			if err := write(out, t.vals[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("assertionid: unsupported value %T", v)
	}
	return nil
}

// lessUTF16 orders strings by UTF-16 code units, as RFC 8785 requires.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeString emits the minimal JSON escaping of RFC 8785: only `"`, `\` and
// control characters below U+0020 are escaped; everything else is raw UTF-8.
func writeString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			out.WriteString(`\"`)
		case r == '\\':
			out.WriteString(`\\`)
		case r == '\b':
			out.WriteString(`\b`)
		case r == '\t':
			out.WriteString(`\t`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\f':
			out.WriteString(`\f`)
		case r == '\r':
			out.WriteString(`\r`)
		case r < 0x20:
			fmt.Fprintf(out, `\u%04x`, r)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
}

// formatNumber is the ECMAScript Number::toString algorithm (RFC 8785 §3.2.2.3).
func formatNumber(f float64) string {
	if f == 0 {
		return "0" // also -0
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	// Shortest round-trip digits: d.ddde±XX.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k, n := len(digits), exp+1
	var body string
	switch {
	case k <= n && n <= 21:
		body = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		body = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		body = "0." + strings.Repeat("0", -n) + digits
	default:
		esign := "+"
		if n-1 < 0 {
			esign = "-"
		}
		abs := n - 1
		if abs < 0 {
			abs = -abs
		}
		if k == 1 {
			body = digits + "e" + esign + strconv.Itoa(abs)
		} else {
			body = digits[:1] + "." + digits[1:] + "e" + esign + strconv.Itoa(abs)
		}
	}
	return sign + body
}
