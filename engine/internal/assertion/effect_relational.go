package assertion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/attest-framework/attest/engine/internal/world"
)

// relatedRef names the record another resource holds for a key: the record in
// Resource whose On field equals the On field of the record being checked.
type relatedRef struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	On       string `json:"on"`
}

func (r *relatedRef) validate() error {
	if r.Resource == "" || r.Field == "" || r.On == "" {
		return fmt.Errorf("related needs resource, field and on")
	}
	return nil
}

// atMostBound is the at_most value of an effect spec: a number, or for sum a
// {"related": {...}} reference. count reads it as an integer, a numeric sum
// as a decimal, and a related sum as a per-key bound.
type atMostBound struct {
	number  *big.Rat
	text    string
	related *relatedRef
}

func (b *atMostBound) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var obj struct {
			Related *relatedRef `json:"related"`
		}
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&obj); err != nil || obj.Related == nil {
			return fmt.Errorf("at_most must be a number or {\"related\": {resource, field, on}}")
		}
		if err := obj.Related.validate(); err != nil {
			return fmt.Errorf("at_most: %w", err)
		}
		b.related = obj.Related
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return fmt.Errorf("at_most must be a number or {\"related\": {resource, field, on}}")
	}
	if err := world.CheckExponent(n.String()); err != nil {
		return fmt.Errorf("at_most: %w", err)
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return fmt.Errorf("at_most must be a finite number")
	}
	b.number, b.text = r, n.String()
	return nil
}

// count is the bound as the integer a count check compares against.
func (b *atMostBound) count() (int, error) {
	if b.related != nil || b.number == nil || !b.number.IsInt() || !b.number.Num().IsInt64() {
		return 0, fmt.Errorf("count: at_most must be an integer")
	}
	return int(b.number.Num().Int64()), nil
}

// evidenceError is a world the check cannot judge: a non-numeric sum field or
// an ambiguous related key. It fails the assertion without "invalid effect
// spec", since the spec is well formed and the snapshot is the problem.
type evidenceError struct{ msg string }

func (e *evidenceError) Error() string { return "effect evidence: " + e.msg }

func evidencef(format string, a ...any) error { return &evidenceError{msg: fmt.Sprintf(format, a...)} }

// relationalViolation is one broken key, ordered by key for stable output.
type relationalViolation struct {
	key, message, expected, actual string
}

// relationalReport picks the first violation by key order and counts the
// rest, mirroring the unique check's "; N more" suffix.
func relationalReport(found []relationalViolation) *effectViolation {
	if len(found) == 0 {
		return nil
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].key < found[j].key })
	first := found[0]
	msg := first.message
	if len(found) > 1 {
		msg += fmt.Sprintf("; %d more violations", len(found)-1)
	}
	return &effectViolation{message: msg, expected: first.expected, actual: first.actual}
}

// relatedIndex maps the canonical On value to the record that holds it. A key
// held by two records is ambiguous and an evidence error.
func relatedIndex(ref *relatedRef, state map[string][]map[string]any) (map[string]map[string]any, error) {
	records, ok := state[ref.Resource]
	if !ok {
		return nil, fmt.Errorf("related resource %q is not in the world (resources: %s)", ref.Resource, strings.Join(sortedKeys(state), ", "))
	}
	index := map[string]map[string]any{}
	for _, r := range records {
		v, present := r[ref.On]
		if !present {
			continue
		}
		k := canonicalJSON(v)
		if _, dup := index[k]; dup {
			return nil, evidencef("%s has more than one record with %s %s, so %s is not a unique key", ref.Resource, ref.On, display(v), ref.On)
		}
		index[k] = r
	}
	return index, nil
}

// numeric reads a record value as an exact decimal. Strings, booleans, null
// and absent fields are not numbers: a sum never treats them as zero, since
// that would let a malformed record hide an overspend.
func numeric(resource, field string, record map[string]any, position int) (*big.Rat, error) {
	v, present := record[field]
	n, isNumber := v.(json.Number)
	if !present || !isNumber {
		got := "missing"
		if present {
			got = display(v)
		}
		return nil, evidencef("%s %s is not a number in record %d (%s); a sum needs numeric values", resource, field, position, got)
	}
	if err := world.CheckExponent(n.String()); err != nil {
		return nil, evidencef("%s %s in record %d: %v", resource, field, position, err)
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return nil, evidencef("%s %s is not a finite number in record %d (%s)", resource, field, position, n.String())
	}
	return r, nil
}

// formatRat prints an exact rational losslessly: whole numbers as integers,
// terminating decimals digit for digit, and any other value as num/den, so two
// different values never print the same.
func formatRat(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	digits, ok := terminatingDigits(r.Denom())
	if !ok {
		return r.Num().String() + "/" + r.Denom().String()
	}
	return r.FloatString(digits)
}

// terminatingDigits returns the fraction digits needed to print 1/den exactly,
// and false when den has a prime factor other than 2 or 5 or needs more than
// maxExactDigits digits.
func terminatingDigits(den *big.Int) (int, bool) {
	digits, _, ok := terminatingDigitsSteps(den)
	return digits, ok
}

// terminatingDigitsSteps is terminatingDigits that also reports how many
// divisions by 5 it ran, which never exceeds maxExactDigits.
func terminatingDigitsSteps(den *big.Int) (digits, steps int, ok bool) {
	twos := int(den.TrailingZeroBits())
	rest := new(big.Int).Rsh(den, uint(twos))
	five := big.NewInt(5)
	rem := new(big.Int)
	quo := new(big.Int)
	fives := 0
	for rest.Cmp(big.NewInt(1)) != 0 {
		if fives >= maxExactDigits {
			return 0, fives, false
		}
		quo.DivMod(rest, five, rem)
		if rem.Sign() != 0 {
			return 0, fives, false
		}
		rest, quo = quo, rest
		fives++
	}
	if twos > maxExactDigits {
		return 0, fives, false
	}
	return max(twos, fives), fives, true
}

// maxExactDigits bounds the fraction digits formatRat prints; a value that
// needs more prints as num/den.
const maxExactDigits = 1024

func checkSum(spec *effectSpec, records []map[string]any, state map[string][]map[string]any) (*effectViolation, string, error) {
	if spec.Field == "" {
		return nil, "", fmt.Errorf("sum needs field, the numeric record field to add up")
	}
	if spec.AtMost == nil {
		return nil, "", fmt.Errorf("sum needs at_most, a number or a related bound")
	}
	if spec.Equals != nil || spec.AtLeast != nil || len(spec.Fields) > 0 || spec.Related != nil {
		return nil, "", fmt.Errorf("sum takes only at_most, field and where")
	}
	if spec.AtMost.related == nil {
		return sumAgainstNumber(spec, records)
	}
	return sumAgainstRelated(spec, records, state)
}

func sumAgainstNumber(spec *effectSpec, records []map[string]any) (*effectViolation, string, error) {
	total := new(big.Rat)
	for i, r := range records {
		if !matches(r, spec.Where) {
			continue
		}
		n, err := numeric(spec.Resource, spec.Field, r, i+1)
		if err != nil {
			return nil, "", err
		}
		total.Add(total, n)
	}
	subject := fmt.Sprintf("%s %s sum %s%s", spec.Resource, spec.Field, formatRat(total), whereText(spec.Where))
	if total.Cmp(spec.AtMost.number) > 0 {
		return &effectViolation{
			message:  fmt.Sprintf("effect violated: %s exceeds %s", subject, spec.AtMost.text),
			expected: "at most " + spec.AtMost.text, actual: formatRat(total),
		}, "", nil
	}
	return nil, fmt.Sprintf("effect holds: %s, at most %s", subject, spec.AtMost.text), nil
}

func sumAgainstRelated(spec *effectSpec, records []map[string]any, state map[string][]map[string]any) (*effectViolation, string, error) {
	ref := spec.AtMost.related
	index, err := relatedIndex(ref, state)
	if err != nil {
		return nil, "", err
	}
	totals := map[string]*big.Rat{}
	labels := map[string]string{}
	var found []relationalViolation
	for i, r := range records {
		if !matches(r, spec.Where) {
			continue
		}
		on, present := r[ref.On]
		if !present {
			found = append(found, relationalViolation{
				key:      "\x00" + fmt.Sprintf("%08d", i+1),
				message:  fmt.Sprintf("effect violated: %s record %d has no %s to relate to %s", spec.Resource, i+1, ref.On, ref.Resource),
				expected: "every record carries " + ref.On, actual: "missing",
			})
			continue
		}
		n, err := numeric(spec.Resource, spec.Field, r, i+1)
		if err != nil {
			return nil, "", err
		}
		k := canonicalJSON(on)
		if totals[k] == nil {
			totals[k] = new(big.Rat)
			labels[k] = display(on)
		}
		totals[k].Add(totals[k], n)
	}
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		total := totals[k]
		owner, ok := index[k]
		if !ok {
			found = append(found, relationalViolation{
				key:      k,
				message:  fmt.Sprintf("effect violated: %s has %s %s but %s has no record with that %s", spec.Resource, ref.On, labels[k], ref.Resource, ref.On),
				expected: "a " + ref.Resource + " record for " + labels[k], actual: "none",
			})
			continue
		}
		bound, err := numeric(ref.Resource, ref.Field, owner, 0)
		if err != nil {
			return nil, "", evidencef("%s %s is not a number for %s %s (%s); a sum bound needs a numeric value",
				ref.Resource, ref.Field, ref.On, labels[k], display(owner[ref.Field]))
		}
		if total.Cmp(bound) > 0 {
			found = append(found, relationalViolation{
				key: k,
				message: fmt.Sprintf("effect violated: %s %s sum %s for %s exceeds %s.%s %s",
					spec.Resource, spec.Field, formatRat(total), labels[k], ref.Resource, ref.Field, formatRat(bound)),
				expected: "at most " + formatRat(bound), actual: formatRat(total),
			})
		}
	}
	if v := relationalReport(found); v != nil {
		return v, "", nil
	}
	return nil, fmt.Sprintf("effect holds: %s %s sum within %s.%s for %d %s values",
		spec.Resource, spec.Field, ref.Resource, ref.Field, len(totals), ref.On), nil
}

func checkMatches(spec *effectSpec, records []map[string]any, state map[string][]map[string]any) (*effectViolation, string, error) {
	if spec.Field == "" {
		return nil, "", fmt.Errorf("matches needs field, the record field that must equal the related one")
	}
	if spec.Related == nil {
		return nil, "", fmt.Errorf("matches needs related, the record to compare against")
	}
	if err := spec.Related.validate(); err != nil {
		return nil, "", err
	}
	if spec.Equals != nil || spec.AtMost != nil || spec.AtLeast != nil || len(spec.Fields) > 0 {
		return nil, "", fmt.Errorf("matches takes only field, related and where")
	}
	ref := spec.Related
	index, err := relatedIndex(ref, state)
	if err != nil {
		return nil, "", err
	}
	var found []relationalViolation
	checked := 0
	for i, r := range records {
		if !matches(r, spec.Where) {
			continue
		}
		checked++
		on, present := r[ref.On]
		if !present {
			found = append(found, relationalViolation{
				key:      "\x00" + fmt.Sprintf("%08d", i+1),
				message:  fmt.Sprintf("effect violated: %s record %d has no %s to relate to %s", spec.Resource, i+1, ref.On, ref.Resource),
				expected: "every record carries " + ref.On, actual: "missing",
			})
			continue
		}
		label := display(on)
		owner, ok := index[canonicalJSON(on)]
		if !ok {
			found = append(found, relationalViolation{
				key:      canonicalJSON(on) + fmt.Sprintf("\x00%08d", i+1),
				message:  fmt.Sprintf("effect violated: %s has %s %s but %s has no record with that %s", spec.Resource, ref.On, label, ref.Resource, ref.On),
				expected: "a " + ref.Resource + " record for " + label, actual: "none",
			})
			continue
		}
		got, gotOK := r[spec.Field]
		want, wantOK := owner[ref.Field]
		if gotOK && wantOK && jsonEqual(got, want) {
			continue
		}
		found = append(found, relationalViolation{
			key: canonicalJSON(on) + fmt.Sprintf("\x00%08d", i+1),
			message: fmt.Sprintf("effect violated: %s %s %s for %s does not match %s.%s %s",
				spec.Resource, spec.Field, presentOrMissing(got, gotOK), label, ref.Resource, ref.Field, presentOrMissing(want, wantOK)),
			expected: presentOrMissing(want, wantOK), actual: presentOrMissing(got, gotOK),
		})
	}
	if v := relationalReport(found); v != nil {
		return v, "", nil
	}
	return nil, fmt.Sprintf("effect holds: %d %s %s match %s.%s", checked, spec.Resource, spec.Field, ref.Resource, ref.Field), nil
}

func presentOrMissing(v any, present bool) string {
	if !present {
		return "(missing)"
	}
	return display(v)
}
