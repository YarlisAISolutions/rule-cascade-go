package analyze

import (
	"os"
	"path/filepath"
	"testing"
)

// Every language rcas supports, natively or through the engine: one decision each, with the
// library that finds it and what it compares.
var languageCases = []struct {
	file, code, library string
	want                facts
}{
	// TypeScript and JavaScript, and their front ends
	{"web/order.ts", "const Order = z.object({ qty: z.number().int().max(10) })", "zod", facts{"qty", "<=", "10"}},
	{"web/order.ts", "qty: yup.number().min(1).max(10),", "yup", facts{"qty", ">=", "1"}},
	{"web/dto.ts", "@Max(10)\nqty: number;", "class-validator", facts{"qty", "<=", "10"}},
	{"web/Form.tsx", `<input {...register("qty", { max: 10 })} />`, "react-hook-form", facts{"qty", "<=", "10"}},
	{"web/Form.tsx", "{total > 10000 && <Approval />}", "react", facts{"total", ">", "10000"}},
	{"web/order.component.ts", "qty: [1, [Validators.required, Validators.max(10)]],", "angular-forms", facts{"qty", "<=", "10"}},
	{"web/order.component.html", `<div *ngIf="total > 10000">approval</div>`, "angular", facts{"total", ">", "10000"}},
	{"web/Order.vue", `<p v-if="total > 10000">approval</p>`, "vue", facts{"total", ">", "10000"}},
	{"web/Order.svelte", "{#if total > 10000}", "svelte", facts{"total", ">", "10000"}},
	{"web/roles.ts", "@Roles('manager')", "nestjs-roles", facts{}},
	// Python
	{"svc/models.py", "    qty: int = Field(ge=1, le=10)", "pydantic", facts{"qty", ">=", "1"}},
	{"svc/models.py", "    qty = models.IntegerField(validators=[MaxValueValidator(10)])", "django", facts{"qty", "<=", "10"}},
	{"svc/schemas.py", "    qty = fields.Int(validate=validate.Range(max=10))", "marshmallow", facts{"qty", "<=", "10"}},
	{"svc/views.py", "@permission_required('orders.approve')", "django-permission", facts{}},
	{"svc/rules.py", "MAX_QTY = 10", "named-limit", facts{"MAX_QTY", "<=", "10"}},
	// Java, Kotlin, Scala
	{"jvm/Order.java", "@Size(min = 2, max = 40)\nprivate String name;", "bean-validation", facts{"name", "<=", "40"}},
	{"jvm/Order.kt", "require(qty <= 10) { \"at most 10\" }", "hand-written", facts{"qty", "<=", "10"}},
	{"jvm/Order.kt", "const val MAX_QTY = 10", "named-limit", facts{"MAX_QTY", "<=", "10"}},
	{"jvm/Tier.kt", "    Tier.GOLD -> 20", "branch", facts{}},
	{"jvm/Screen.kt", "Button(enabled = age >= 18, onClick = {}) {", "compose", facts{"age", ">=", "18"}},
	// Go
	{"go/order.go", "\tQty int `json:\"qty\" validate:\"min=1,max=10\"`", "go-validator", facts{"Qty", ">=", "1"}},
	{"go/order.go", "validation.Field(&o.Qty, validation.Required, validation.Max(10)),", "ozzo-validation", facts{"Qty", "<=", "10"}},
	{"go/limits.go", "const MaxQuantity = 10", "named-limit", facts{"MaxQuantity", "<=", "10"}},
	// C# and Razor
	{"net/Order.cs", "[Range(1, 10)]\npublic int Quantity { get; set; }", "data-annotations", facts{"Quantity", "<=", "10"}},
	{"net/OrderValidator.cs", "RuleFor(x => x.Quantity).LessThanOrEqualTo(10);", "fluentvalidation", facts{"Quantity", "<=", "10"}},
	{"net/Limits.cs", "public const int MaxQuantity = 10;", "named-limit", facts{"MaxQuantity", "<=", "10"}},
	{"net/OrdersController.cs", "[Authorize(Roles = \"Manager\")]", "aspnet-authorize", facts{}},
	{"net/Order.razor", "@if (Total > 10000)", "blazor", facts{"Total", ">", "10000"}},
	// Ruby
	{"rb/order.rb", "validates :quantity, numericality: { less_than_or_equal_to: 10 }", "rails", facts{"quantity", "<=", "10"}},
	{"rb/order.rb", "  when \"GOLD\" then 20", "branch", facts{}},
	{"rb/orders_controller.rb", "authorize @order", "rails-authorization", facts{}},
	// PHP
	{"php/OrderRequest.php", "'quantity' => 'required|integer|max:10',", "laravel", facts{"quantity", "<=", "10"}},
	{"php/OrderPolicy.php", "if (Gate::denies('approve-order')) {", "laravel-gate", facts{}},
	// Rust
	{"rs/order.rs", "#[validate(range(min = 1, max = 10))]\npub quantity: u32,", "validator-rs", facts{"quantity", "<=", "10"}},
	{"rs/order.rs", "return Err(ValidationError::TooMany);", "hand-written", facts{}},
	// Swift
	{"ios/Limits.swift", "static let maxQuantity = 10", "named-limit", facts{"maxQuantity", "<=", "10"}},
	{"ios/OrderView.swift", ".disabled(age < 18)", "swiftui", facts{"age", "<", "18"}},
	{"ios/Tier.swift", "case .gold:", "branch", facts{}},
	// Dart and Flutter
	{"app/limits.dart", "static const maxQuantity = 10;", "named-limit", facts{"maxQuantity", "<=", "10"}},
	{"app/form.dart", "validator: (v) => int.parse(v!) > 10 ? 'at most 10' : null,", "flutter", facts{}},
	// C and C++
	{"cpp/limits.h", "#define MAX_QUANTITY 10", "named-limit", facts{"MAX_QUANTITY", "<=", "10"}},
	{"cpp/limits.hpp", "constexpr int kMaxQuantity = 10;", "named-limit", facts{"kMaxQuantity", "<=", "10"}},
	{"cpp/order.cpp", "if (qty > 10) throw std::invalid_argument(\"at most 10\");", "hand-written", facts{"qty", ">", "10"}},
	// Rule engines
	{"rules/orders.drl", "rule \"Large order\"", "drools", facts{}},
	{"rules/orders.dmn", `<decision id="approval" name="Approval">`, "dmn", facts{}},
}

func TestEveryLanguage(t *testing.T) {
	for _, c := range languageCases {
		dir := t.TempDir()
		path := filepath.Join(dir, filepath.FromSlash(c.file))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(c.code+"\n"), 0o644)
		r := Scan(dir, nil, Options{})
		var got *Hit
		for i := range r.Hits {
			if r.Hits[i].Library == c.library {
				got = &r.Hits[i]
				break
			}
		}
		if got == nil {
			t.Errorf("%s %q: no %s hit; got %+v", c.file, c.code, c.library, r.Hits)
			continue
		}
		if (facts{got.Name, got.Op, got.Value}) != c.want {
			t.Errorf("%s %q (%s): compares %q %q %q, want %+v", c.file, c.code, c.library, got.Name, got.Op, got.Value, c.want)
		}
	}
}

// The same rule in a backend and a front end of different languages is one candidate, with drift.
func TestDriftAcrossStacks(t *testing.T) {
	for _, pair := range [][2]struct{ file, code string }{
		{{"api/models.py", "    quantity: int = Field(le=10)"}, {"web/Order.vue", `<p v-if="quantity > 20">too many</p>`}},
		{{"api/order.go", "\tQuantity int `validate:\"max=10\"`"}, {"web/order.component.ts", "quantity: [1, [Validators.max(20)]],"}},
		{{"api/Order.cs", "[Range(1, 10)]\npublic int Quantity { get; set; }"}, {"web/Order.svelte", "{#if quantity > 20}"}},
		{{"api/order.rb", "validates :quantity, numericality: { less_than_or_equal_to: 10 }"}, {"app/limits.dart", "static const maxQuantity = 20;"}},
		{{"api/OrderRequest.php", "'quantity' => 'required|max:10',"}, {"ios/Limits.swift", "static let maxQuantity = 20"}},
		{{"api/order.rs", "#[validate(range(max = 10))]\npub quantity: u32,"}, {"web/Form.tsx", `<input {...register("quantity", { max: 20 })} />`}},
	} {
		dir := t.TempDir()
		for _, f := range pair {
			path := filepath.Join(dir, filepath.FromSlash(f.file))
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, []byte(f.code+"\n"), 0o644)
		}
		r := Scan(dir, nil, Options{})
		if len(r.Candidates) != 1 || r.Candidates[0].Key != "quantity" || !r.Candidates[0].Drift {
			t.Errorf("%s and %s: want one drifting quantity candidate, got %+v (hits %+v)", pair[0].file, pair[1].file, r.Candidates, r.Hits)
		}
	}
}

func TestModulesOfEveryBuildTool(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"jvm/pom.xml", "gradle/build.gradle.kts", "scala/build.sbt", "web/package.json", "go/go.mod",
		"py/pyproject.toml", "rs/Cargo.toml", "net/Shop.csproj", "php/composer.json", "rb/Gemfile", "ios/Package.swift",
		"app/pubspec.yaml", "cpp/CMakeLists.txt"} {
		path := filepath.Join(dir, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("\n"), 0o644)
	}
	builds := map[string]string{}
	for _, m := range FindModules(dir) {
		builds[m.Path] = m.Build
	}
	for path, build := range map[string]string{"jvm": "maven", "gradle": "gradle", "scala": "sbt", "web": "npm", "go": "go", "py": "python",
		"rs": "cargo", "net": "dotnet", "php": "composer", "rb": "bundler", "ios": "swift", "app": "dart", "cpp": "cmake"} {
		if builds[path] != build {
			t.Errorf("%s: build %q, want %q", path, builds[path], build)
		}
	}
}
