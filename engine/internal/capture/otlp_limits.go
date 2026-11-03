package capture

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/attest-ai/attest/engine/internal/trace"
)

// errOverLimit marks an OTLP body that exceeds the receiver's caps.
var errOverLimit = errors.New("OTLP body over limit")

// OTLP protobuf field numbers walked by checkOTLPLimits
// (opentelemetry-proto trace/v1, common/v1, resource/v1).
const (
	fieldResourceSpans = 1 // TracesData / ExportTraceServiceRequest
	fieldResource      = 1 // ResourceSpans
	fieldScopeSpans    = 2 // ResourceSpans
	fieldResourceAttrs = 1 // Resource
	fieldScope         = 1 // ScopeSpans
	fieldSpans         = 2 // ScopeSpans
	fieldScopeAttrs    = 3 // InstrumentationScope
	fieldSpanAttrs     = 9 // Span
	fieldSpanEvents    = 11
	fieldSpanLinks     = 13
	fieldEventAttrs    = 3 // Span.Event
	fieldLinkAttrs     = 4 // Span.Link
	fieldKeyValueValue = 2 // KeyValue
	fieldAnyArray      = 5 // AnyValue
	fieldAnyKVList     = 6
	fieldArrayValues   = 1 // ArrayValue
	fieldKVListValues  = 1 // KeyValueList
)

// limitWalker counts every message it visits (resource spans, scope spans,
// spans, events, links, attributes and nested values) in raw protobuf without
// decoding it, so a small compressed body that would expand into millions of
// messages is refused before any of them is allocated.
type limitWalker struct {
	spans  int
	values int
}

// checkOTLPLimits enforces the OTLP/JSON parser's caps on an http/protobuf
// body: total spans, attributes per span, total attribute values and value
// nesting depth.
func checkOTLPLimits(body []byte) error {
	w := &limitWalker{}
	return w.message(body, func(num protowire.Number, b []byte) error {
		if num != fieldResourceSpans {
			return nil
		}
		if err := w.count(0); err != nil {
			return err
		}
		return w.resourceSpans(b)
	})
}

// message calls fn for each length-delimited field of b and skips the rest.
func (w *limitWalker) message(b []byte, fn func(protowire.Number, []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
			continue
		}
		v, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if err := fn(num, v); err != nil {
			return err
		}
	}
	return nil
}

func (w *limitWalker) resourceSpans(b []byte) error {
	return w.message(b, func(num protowire.Number, v []byte) error {
		switch num {
		case fieldResource:
			return w.attrsIn(v, fieldResourceAttrs)
		case fieldScopeSpans:
			if err := w.count(0); err != nil {
				return err
			}
			return w.scopeSpans(v)
		}
		return nil
	})
}

func (w *limitWalker) scopeSpans(b []byte) error {
	return w.message(b, func(num protowire.Number, v []byte) error {
		switch num {
		case fieldScope:
			return w.attrsIn(v, fieldScopeAttrs)
		case fieldSpans:
			w.spans++
			if w.spans > trace.MaxOTLPSpans {
				return fmt.Errorf("%w: more than %d spans", errOverLimit, trace.MaxOTLPSpans)
			}
			if err := w.count(0); err != nil {
				return err
			}
			return w.span(v)
		}
		return nil
	})
}

func (w *limitWalker) span(b []byte) error {
	attrs := 0
	return w.message(b, func(num protowire.Number, v []byte) error {
		switch num {
		case fieldSpanAttrs:
			attrs++
			if attrs > trace.MaxAttrsPerSpan {
				return fmt.Errorf("%w: a span has more than %d attributes", errOverLimit, trace.MaxAttrsPerSpan)
			}
			return w.keyValue(v, 1)
		case fieldSpanEvents:
			if err := w.count(0); err != nil {
				return err
			}
			return w.attrsIn(v, fieldEventAttrs)
		case fieldSpanLinks:
			if err := w.count(0); err != nil {
				return err
			}
			return w.attrsIn(v, fieldLinkAttrs)
		}
		return nil
	})
}

// attrsIn counts the KeyValue attributes in field num of message b.
func (w *limitWalker) attrsIn(b []byte, num protowire.Number) error {
	return w.message(b, func(n protowire.Number, v []byte) error {
		if n != num {
			return nil
		}
		return w.keyValue(v, 1)
	})
}

func (w *limitWalker) count(depth int) error {
	w.values++
	if w.values > trace.MaxOTLPValues {
		return fmt.Errorf("%w: more than %d messages and attribute values", errOverLimit, trace.MaxOTLPValues)
	}
	if depth > trace.MaxAnyValueDepth {
		return fmt.Errorf("%w: attribute values nested deeper than %d", errOverLimit, trace.MaxAnyValueDepth)
	}
	return nil
}

func (w *limitWalker) keyValue(b []byte, depth int) error {
	if err := w.count(depth); err != nil {
		return err
	}
	return w.message(b, func(num protowire.Number, v []byte) error {
		if num != fieldKeyValueValue {
			return nil
		}
		return w.anyValue(v, depth)
	})
}

func (w *limitWalker) anyValue(b []byte, depth int) error {
	return w.message(b, func(num protowire.Number, v []byte) error {
		switch num {
		case fieldAnyArray:
			return w.message(v, func(n protowire.Number, item []byte) error {
				if n != fieldArrayValues {
					return nil
				}
				if err := w.count(depth + 1); err != nil {
					return err
				}
				return w.anyValue(item, depth+1)
			})
		case fieldAnyKVList:
			return w.message(v, func(n protowire.Number, kv []byte) error {
				if n != fieldKVListValues {
					return nil
				}
				return w.keyValue(kv, depth+1)
			})
		}
		return nil
	})
}
