package analyze

import (
	"regexp"
	"strings"
	"unicode"
)

// facts is what one line compares: a name, an operator and a number.
type facts struct{ Name, Op, Value string }

// literal reports whether the line compares a real name with a number that is likely a business
// value: not a one-letter loop variable, and not 0, 1 or -1.
func (f facts) literal() bool {
	if f.Value == "" || len(lastSegment(f.Name)) < 2 {
		return false
	}
	switch strings.TrimSuffix(strings.TrimSuffix(f.Value, ".0"), ".00") {
	case "0", "1", "-1":
		return false
	}
	return true
}

var (
	number = `(-?\d+(?:\.\d+)?)`
	// attributes on the line above a field: @Max(10) (Java, Kotlin, class-validator), [Range(1, 10)]
	// (C#), #[validate(range(max = 10))] (Rust)
	annotationArgs = regexp.MustCompile(`@(Min|Max|DecimalMin|DecimalMax|MinLength|MaxLength|Size|Length)\(\s*([^)]*)\)` +
		`|\[(Range|MaxLength|MinLength|StringLength)\(\s*([^)\]]*)\)` + `|#\[validate\(\s*(range|length)\(([^)]*)\)`)
	argMax    = regexp.MustCompile(`\b[Mm]ax(?:imum)?\s*=\s*"?` + number)
	argMin    = regexp.MustCompile(`\b[Mm]in(?:imum)?\s*=\s*"?` + number)
	argFirst  = regexp.MustCompile(`^(?:value\s*=\s*)?"?` + number)
	argTwo    = regexp.MustCompile(`^\s*"?` + number + `"?\s*,\s*"?` + number)
	csField   = regexp.MustCompile(`\b(\w+)\s*\{\s*get\b`)
	tsField   = regexp.MustCompile(`\b(\w+)\s*[!?]?\s*:\s*[\w\[\]<>|.:? ]+[;,]?\s*$`)
	javaField = regexp.MustCompile(`\b(\w+)\s*(?:=[^;]*)?;\s*$`)
	ktField   = regexp.MustCompile(`\b(?:val|var)\s+(\w+)`)
	constant  = regexp.MustCompile(`\b([A-Z0-9_]*(?:MAX|MIN|LIMIT|THRESHOLD|RATE|FEE|DISCOUNT|AGE|DAYS|AMOUNT|QUANTITY|QTY)[A-Z0-9_]*)\s*(?::\s*[\w<>.]+\s*)?=\s*` + number +
		`|#define\s+(\w+)\s+` + number +
		`|\b(?:const|constexpr|static|final|let|val|readonly)\s+(?:[\w<>?:]+\s+)*(k?(?:Max|Min|max|min)[A-Z]\w*|\w+(?:Limit|Threshold))\s*(?::\s*[\w<>.?]+\s*)?=\s*` + number)
	annotation = regexp.MustCompile(`@\w+(\([^)]*\))?|\[\w+(\([^)\]]*\))?\]|#\[[^\]]*\]`)
	comparison = regexp.MustCompile(`([A-Za-z_$][\w$.]*(?:\(\))?)\s*(<=|>=|===|!==|==|!=|<|>)\s*` + number + `\b`)
)

// sameLine reads a name and a bound from one line, for the libraries that keep both there.
// name, bound and value are submatch indexes; bound names an entry of boundOp.
type sameLine struct {
	re                 *regexp.Regexp
	name, bound, value int
}

var sameLines = []sameLine{
	// Zod, Yup, Joi: qty: z.number().max(10)
	{regexp.MustCompile(`(\w+)\s*:\s*(?:z|yup|Joi)\.\w+\([^)]*\)(?:\s*\.\w+\([^)]*\))*?\s*\.(max|lte|lt|min|gte|gt|length)\(\s*` + number), 1, 2, 3},
	// react-hook-form: register("qty", { max: 10 }), <Controller name="qty" rules={{ max: 10 }}
	{regexp.MustCompile(`(?:\bregister\(\s*|\bname=)["']([\w.\[\]]+)["'].*?\b(max|maxLength|min|minLength)\s*:\s*(?:\{\s*value\s*:\s*)?` + number), 1, 2, 3},
	// Angular reactive forms: qty: [0, [Validators.max(10)]]
	{regexp.MustCompile(`(\w+)\s*:\s*(?:new\s+FormControl\(|\[).*?Validators\.(max|min|maxLength|minLength)\(\s*` + number), 1, 2, 3},
	// Pydantic, marshmallow, attrs: qty: int = Field(le=10), qty = fields.Int(validate=Range(max=10))
	{regexp.MustCompile(`^\s*(\w+)\s*[:=].*?\b(le|ge|lt|gt|max_length|min_length|max|min)\s*=\s*` + number), 1, 2, 3},
	// Django: qty = models.IntegerField(validators=[MaxValueValidator(10)])
	{regexp.MustCompile(`^\s*(\w+)\s*[:=].*?\b(Max|Min)(?:Value|Length)Validator\(\s*` + number), 1, 2, 3},
	// Rails: validates :qty, numericality: { less_than_or_equal_to: 10 }, length: { maximum: 40 }
	{regexp.MustCompile(`validates\s+:(\w+).*?\b(less_than_or_equal_to|less_than|greater_than_or_equal_to|greater_than|maximum|minimum|equal_to|is)\s*:\s*` + number), 1, 2, 3},
	// Laravel: 'qty' => 'required|integer|max:10'
	{regexp.MustCompile(`['"](\w+)['"]\s*=>\s*\[?\s*['"][^'"]*?\b(max|min|lt|lte|gt|gte|size):` + number), 1, 2, 3},
	// Go struct tags: Qty int `validate:"min=1,max=10"` (also gin's binding:"...")
	{regexp.MustCompile("^\\s*(\\w+)\\s+[\\w.\\[\\]*]+\\s+`[^`]*(?:validate|binding):\"[^\"]*?\\b(max|min|lte|gte|lt|gt|len)=" + number), 1, 2, 3},
	// ozzo-validation: validation.Field(&o.Qty, validation.Max(10))
	{regexp.MustCompile(`validation\.Field\(\s*&\w+\.(\w+).*?validation\.(Max|Min)\(\s*` + number), 1, 2, 3},
	// FluentValidation: RuleFor(x => x.Qty).LessThanOrEqualTo(10)
	{regexp.MustCompile(`RuleFor\(\s*\w+\s*=>\s*\w+\.(\w+)\s*\).*?\.(LessThanOrEqualTo|LessThan|GreaterThanOrEqualTo|GreaterThan|MaximumLength|MinimumLength|Equal)\(\s*` + number), 1, 2, 3},
}

var boundOp = map[string]string{"max": "<=", "lte": "<=", "le": "<=", "maxlength": "<=", "max_length": "<=", "lt": "<",
	"min": ">=", "gte": ">=", "ge": ">=", "minlength": ">=", "min_length": ">=", "gt": ">", "length": "=", "len": "=", "size": "=", "is": "=",
	"decimalmax": "<=", "decimalmin": ">=", "stringlength": "<=", "maximum": "<=", "minimum": ">=", "equal_to": "=", "equal": "=",
	"less_than_or_equal_to": "<=", "less_than": "<", "greater_than_or_equal_to": ">=", "greater_than": ">",
	"lessthanorequalto": "<=", "lessthan": "<", "greaterthanorequalto": ">=", "greaterthan": ">", "maximumlength": "<=", "minimumlength": ">="}

// readFacts reads what line i compares. An attribute names its field on the same line or on one
// of the next three.
func readFacts(lines []string, i int) facts {
	line := lines[i]
	if m := annotationArgs.FindStringSubmatch(line); m != nil {
		kind, args := strings.ToLower(m[1]+m[3]+m[5]), m[2]+m[4]+m[6]
		f := facts{Name: fieldAfter(line[strings.Index(line, m[0])+len(m[0]):], lines, i)}
		switch {
		case argMax.MatchString(args):
			f.Op, f.Value = "<=", argMax.FindStringSubmatch(args)[1]
		case argMin.MatchString(args):
			f.Op, f.Value = ">=", argMin.FindStringSubmatch(args)[1]
		case (kind == "range" || kind == "length" || kind == "size") && argTwo.MatchString(args):
			f.Op, f.Value = "<=", argTwo.FindStringSubmatch(args)[2] // Range(1, 10), @Length(2, 40): the upper bound
		case argFirst.MatchString(args) && boundOp[kind] != "":
			f.Op, f.Value = boundOp[kind], argFirst.FindStringSubmatch(args)[1]
		}
		if f.Value != "" {
			return f
		}
	}
	for _, r := range sameLines {
		if m := r.re.FindStringSubmatch(line); m != nil {
			if op := boundOp[strings.ToLower(m[r.bound])]; op != "" {
				return facts{m[r.name], op, m[r.value]}
			}
		}
	}
	if m := constant.FindStringSubmatch(line); m != nil {
		name, value := m[1]+m[3]+m[5], m[2]+m[4]+m[6]
		op, lower := "=", strings.ToLower(name)
		if strings.Contains(lower, "min") {
			op = ">="
		} else if strings.Contains(lower, "max") || strings.Contains(lower, "limit") {
			op = "<="
		}
		return facts{name, op, value}
	}
	if m := comparison.FindStringSubmatch(line); m != nil {
		return facts{strings.TrimSuffix(m[1], "()"), m[2], m[3]}
	}
	return facts{}
}

// fieldAfter finds the field an annotation is on: the rest of its line, else the next lines.
func fieldAfter(rest string, lines []string, i int) string {
	candidates := []string{rest}
	for j := i + 1; j < len(lines) && j <= i+3; j++ {
		next := strings.TrimSpace(lines[j])
		if strings.HasPrefix(next, "@") && !strings.Contains(next, ";") {
			continue
		}
		candidates = append(candidates, next)
		break
	}
	for _, c := range candidates {
		c = strings.TrimSpace(annotation.ReplaceAllString(c, ""))
		if c == "" {
			continue
		}
		for _, re := range []*regexp.Regexp{csField, ktField, tsField, javaField} {
			if m := re.FindStringSubmatch(c); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

func lastSegment(name string) string {
	name = strings.TrimSuffix(name, "()")
	if i := strings.LastIndexAny(name, ".$"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

var (
	stopWords = map[string]bool{"max": true, "min": true, "maximum": true, "minimum": true, "limit": true, "threshold": true,
		"allowed": true, "default": true, "value": true, "the": true, "cap": true, "k": true, "get": true, "is": true, "has": true}
	synonyms = map[string]string{"qty": "quantity", "amt": "amount", "cnt": "count"}
)

// Key normalises a name so that the same concept matches across languages and styles:
// MAX_ORDER_QUANTITY, maxOrderQuantity, order.quantity and orderQty all give "order-quantity"
// or "quantity" (the last word set of the last segment, without max, min, limit and the like).
func Key(name string) string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			w := strings.ToLower(string(cur))
			if s, ok := synonyms[w]; ok {
				w = s
			}
			if !stopWords[w] {
				words = append(words, w)
			}
			cur = cur[:0]
		}
	}
	seg := []rune(lastSegment(name))
	for i, ch := range seg {
		switch {
		case ch == '_' || ch == '-' || !unicode.IsLetter(ch) && !unicode.IsDigit(ch):
			flush()
		case unicode.IsUpper(ch) && i > 0 && (unicode.IsLower(seg[i-1]) || i+1 < len(seg) && unicode.IsLower(seg[i+1]) && unicode.IsUpper(seg[i-1])):
			flush()
			cur = append(cur, ch)
		default:
			cur = append(cur, ch)
		}
	}
	flush()
	return strings.Join(words, "-")
}
