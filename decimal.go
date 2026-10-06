package rulecascade

import (
	"errors"
	"math/big"
	"strings"
)

// Decimal arithmetic for expressions (specification section 4.2): 34 significant digits, round
// half even. The exponent limits are those of the reference implementation, not of the IEEE
// decimal128 interchange format, so the two agree on every value a ruleset can produce.
const (
	precision     = 34 // arithmetic inside an expression
	wirePrecision = 15 // numbers leaving the engine
	maxExponent   = 999999
	minExponent   = -999999
)

var (
	errOverflow = errors.New("overflowed")
	bigTen      = big.NewInt(10)
)

// decimal is (-1)^neg * coef * 10^exp. coef is never negative and is never modified in place.
type decimal struct {
	neg  bool
	coef *big.Int
	exp  int
}

var decimalZero = decimal{coef: new(big.Int)}

func decimalFromInt(n int64) decimal {
	return decimal{neg: n < 0, coef: new(big.Int).Abs(big.NewInt(n))}
}

// parseDecimal reads a number written as JSON writes it, exactly.
func parseDecimal(s string) (decimal, bool) {
	d := decimal{}
	if strings.HasPrefix(s, "-") {
		d.neg, s = true, s[1:]
	}
	mantissa, exponent := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa, exponent = s[:i], s[i+1:]
		if exponent == "" {
			return d, false
		}
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	if whole+fraction == "" {
		return d, false
	}
	coef, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok || coef.Sign() < 0 {
		return d, false
	}
	d.coef, d.exp = coef, -len(fraction)
	if exponent != "" {
		e, ok := new(big.Int).SetString(exponent, 10)
		if !ok || !e.IsInt64() || e.Int64() > 2*maxExponent || e.Int64() < 2*minExponent {
			return d, false
		}
		d.exp += int(e.Int64())
	}
	return d, true
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(bigTen, big.NewInt(int64(n)), nil)
}

func (d decimal) isZero() bool { return d.coef.Sign() == 0 }

// digits is the number of decimal digits of the coefficient; zero has one.
func (d decimal) digits() int { return len(d.coef.Text(10)) }

// adjusted is the exponent of the most significant digit.
func (d decimal) adjusted() int { return d.exp + d.digits() - 1 }

func (d decimal) negate() decimal { return decimal{neg: !d.neg, coef: d.coef, exp: d.exp} }

// rescale gives the value at another exponent, rounded half even when digits are dropped.
func (d decimal) rescale(exp int) decimal {
	switch {
	case exp == d.exp || d.isZero():
		return decimal{neg: d.neg, coef: d.coef, exp: exp}
	case exp < d.exp:
		return decimal{neg: d.neg, coef: new(big.Int).Mul(d.coef, pow10(d.exp-exp)), exp: exp}
	}
	drop := exp - d.exp
	if drop > d.digits() { // less than a tenth of the last place that is kept
		return decimal{neg: d.neg, coef: new(big.Int), exp: exp}
	}
	unit := pow10(drop)
	q, r := new(big.Int).QuoRem(d.coef, unit, new(big.Int))
	switch r.Lsh(r, 1).Cmp(unit) {
	case 1:
		q.Add(q, big.NewInt(1))
	case 0:
		if q.Bit(0) == 1 {
			q.Add(q, big.NewInt(1))
		}
	}
	return decimal{neg: d.neg, coef: q, exp: exp}
}

// round keeps at most prec significant digits, round half even.
func (d decimal) round(prec int) decimal {
	if d.isZero() {
		return d
	}
	exp := d.digits() + d.exp - prec
	if tiny := minExponent - prec + 1; exp < tiny { // the smallest exponent a result can have
		exp = tiny
	}
	if d.exp < exp {
		return d.rescale(exp)
	}
	return d
}

// fix finishes an arithmetic result: round to the working precision and refuse a number too large.
func (d decimal) fix() (decimal, error) {
	d = d.round(precision)
	if !d.isZero() && d.adjusted() > maxExponent {
		return d, errOverflow
	}
	return d, nil
}

func (d decimal) add(o decimal) (decimal, error) {
	if d.isZero() {
		return o.fix()
	}
	if o.isZero() {
		return d.fix()
	}
	hi, lo := d, o
	if hi.exp < lo.exp {
		hi, lo = lo, hi
	}
	// A term far below the last digit that survives rounding only matters by its sign: replace it
	// with one unit just under that digit, so the exponents never have to be aligned over a
	// huge distance.
	if e := hi.exp + min(-1, hi.digits()-precision-2); lo.adjusted() < e {
		lo = decimal{neg: lo.neg, coef: big.NewInt(1), exp: e}
	}
	a := new(big.Int).Mul(hi.coef, pow10(hi.exp-lo.exp))
	b := new(big.Int).Set(lo.coef)
	if hi.neg {
		a.Neg(a)
	}
	if lo.neg {
		b.Neg(b)
	}
	a.Add(a, b)
	return decimal{neg: a.Sign() < 0, coef: a.Abs(a), exp: lo.exp}.fix()
}

func (d decimal) sub(o decimal) (decimal, error) { return d.add(o.negate()) }

func (d decimal) mul(o decimal) (decimal, error) {
	return decimal{neg: d.neg != o.neg, coef: new(big.Int).Mul(d.coef, o.coef), exp: d.exp + o.exp}.fix()
}

// div divides by a non-zero number.
func (d decimal) div(o decimal) (decimal, error) {
	if d.isZero() {
		return decimalZero, nil
	}
	// enough digits for a correctly rounded quotient, plus one that records an inexact result
	shift := o.digits() - d.digits() + precision + 1
	a, b := d.coef, o.coef
	if shift >= 0 {
		a = new(big.Int).Mul(a, pow10(shift))
	} else {
		b = new(big.Int).Mul(b, pow10(-shift))
	}
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if r.Sign() != 0 && new(big.Int).Rem(q, big.NewInt(5)).Sign() == 0 {
		q.Add(q, big.NewInt(1)) // never leave an inexact quotient looking like an exact half
	}
	return decimal{neg: d.neg != o.neg, coef: q, exp: d.exp - o.exp - shift}.fix()
}

// rem is the remainder of truncated division by a non-zero number; the sign follows d. It fails
// when the whole part of the quotient needs more digits than the working precision.
func (d decimal) rem(o decimal) (decimal, error) {
	if d.isZero() {
		return decimalZero, nil
	}
	gap := d.adjusted() - o.adjusted()
	if gap <= -2 { // |d| < |o|
		return d.fix()
	}
	if gap > precision {
		return d, errOverflow
	}
	a, b := d.coef, o.coef
	if d.exp >= o.exp {
		a = new(big.Int).Mul(a, pow10(d.exp-o.exp))
	} else {
		b = new(big.Int).Mul(b, pow10(o.exp-d.exp))
	}
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if len(q.Text(10)) > precision {
		return d, errOverflow
	}
	return decimal{neg: d.neg, coef: r, exp: min(d.exp, o.exp)}.fix()
}

// cmp compares exactly: -1, 0 or 1.
func (d decimal) cmp(o decimal) int {
	sign := func(x decimal) int {
		switch {
		case x.isZero():
			return 0
		case x.neg:
			return -1
		}
		return 1
	}
	sd, so := sign(d), sign(o)
	if sd != so || sd == 0 {
		return compareInts(sd, so)
	}
	if ad, ao := d.adjusted(), o.adjusted(); ad != ao {
		return sd * compareInts(ad, ao)
	}
	a, b := d.coef, o.coef
	if d.exp > o.exp {
		a = new(big.Int).Mul(a, pow10(d.exp-o.exp))
	} else if o.exp > d.exp {
		b = new(big.Int).Mul(b, pow10(o.exp-d.exp))
	}
	return sd * a.Cmp(b)
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// quantize rounds half even to a number of decimal places. Like the other operations it fails
// when the result needs more digits than the working precision.
func (d decimal) quantize(places int) (decimal, error) {
	if d.isZero() {
		return decimal{coef: d.coef, exp: -places}, nil
	}
	if d.adjusted()+places+1 > precision {
		return d, errOverflow
	}
	r := d.rescale(-places)
	if r.digits() > precision {
		return d, errOverflow
	}
	return r, nil
}

// isWhole reports whether the value has no fractional part.
func (d decimal) isWhole() bool {
	if d.exp >= 0 || d.isZero() {
		return true
	}
	if -d.exp >= d.digits() {
		return false
	}
	return new(big.Int).Rem(d.coef, pow10(-d.exp)).Sign() == 0
}

// toInt converts a whole, non-negative value; values beyond the int range saturate.
func (d decimal) toInt() int {
	const most = int(^uint(0) >> 1)
	if d.isZero() {
		return 0
	}
	if d.adjusted() > 18 {
		return most
	}
	n := d.rescale(0).coef
	if !n.IsInt64() || n.Int64() > int64(most) {
		return most
	}
	return int(n.Int64())
}

// String is the canonical text of specification section 6: plain decimal notation, no exponent,
// no trailing zeros, and "0" for zero.
func (d decimal) String() string {
	if d.isZero() {
		return "0"
	}
	text := d.coef.Text(10)
	trimmed := strings.TrimRight(text, "0")
	exp := d.exp + len(text) - len(trimmed)
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	switch point := len(trimmed) + exp; {
	case exp >= 0:
		b.WriteString(trimmed)
		b.WriteString(strings.Repeat("0", exp))
	case point > 0:
		b.WriteString(trimmed[:point])
		b.WriteByte('.')
		b.WriteString(trimmed[point:])
	default:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -point))
		b.WriteString(trimmed)
	}
	return b.String()
}
