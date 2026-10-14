package main

import (
	"strings"
	"testing"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// evalJS is the type the scan gives what a file of src exports.
func evalJS(t *testing.T, src string) *jsType {
	t.Helper()

	ast, err := js.Parse(parse.NewInputString(src), js.Options{})
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}

	f := readFile("server/x.js", ast)
	e := newEvaluator(&program{files: map[string]*jsFile{f.path: f}}, &models{byClass: map[*jsClass]*model{}, byName: map[string]*model{}})

	return e.exports(f)
}

// shape is a type written short: "list of integer", "string?", "record{a,b}".
func shape(t *jsType) string {
	names := []string{"unknown", "null", "string", "number", "integer", "boolean", "date", "list", "record", "class", "instance", "function", "request", "response", "mixed"}
	out := names[t.kind]
	switch t.kind {
	case arrayKind:
		out += " of " + shape(t.elem)
	case objectKind:
		fields := make([]string, 0, len(t.props))
		for _, p := range t.props {
			fields = append(fields, p.name)
		}
		out += "{" + strings.Join(fields, ",") + "}"
	default:
	}
	if t.nullable && t.kind != nullKind {
		out += "?"
	}

	return out
}

func TestEval(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		// text, numbers and dates
		`'a'.split(',')`:           "list of string",
		`'a'.includes('b')`:        "boolean",
		`'a'.indexOf('b')`:         "integer",
		`'a'.localeCompare('b')`:   "integer",
		`'a'.toLowerCase()`:        "string",
		`'a'.length`:               "integer",
		`(1.5).toFixed(2)`:         "string",
		`new Date()`:               "date",
		`new Date().valueOf()`:     "integer",
		`new Date().toISOString()`: "string",
		"`a${1}`":                  "string",

		// operators
		`typeof 1`:       "string",
		`!1`:             "boolean",
		`-1`:             "integer",
		`-1.5`:           "number",
		`1 + 'a'`:        "string",
		`1 + 2`:          "integer",
		`1 + 2.5`:        "number",
		`1 / 2`:          "number",
		`3 % 2`:          "integer",
		`1 & 2`:          "integer",
		`1 < 2`:          "boolean",
		`true ? 1 : 'a'`: "mixed",
		`null ?? 'a'`:    "string",
		`null || 2`:      "integer",
		`undefined`:      "null",
		`void 0`:         "null",

		// lists
		`[1, 2].map((n) => n * 2)`:       "list of integer",
		`[[1]].flatMap((n) => n)`:        "list of integer",
		`[1].filter((n) => n > 0)`:       "list of integer",
		`[1].find((n) => n > 0)`:         "integer?",
		`[1].some((n) => n > 0)`:         "boolean",
		`[1].indexOf(1)`:                 "integer",
		`['a'].join(',')`:                "string",
		`[1].reduce((a, n) => a + n, 0)`: "integer",
		`[1].forEach((n) => n)`:          "null",
		`[...[1], 2]`:                    "list of integer",
		`[1, 2].length`:                  "integer",
		`[1].keys()`:                     "list of integer",

		// the language's own objects
		`Object.keys({ a: 1 })`:                "list of string",
		`Object.values({ a: 1 })`:              "list of integer",
		`Object.assign({ a: 1 }, { b: 'x' })`:  "record{a,b}",
		`JSON.stringify({})`:                   "string",
		`JSON.parse('{}')`:                     "unknown",
		`Math.round(1.5)`:                      "integer",
		`Math.random()`:                        "number",
		`Date.now()`:                           "integer",
		`Number('1')`:                          "number",
		`Number.isInteger(1)`:                  "boolean",
		`parseInt('1')`:                        "integer",
		`String(1)`:                            "string",
		`Boolean(1)`:                           "boolean",
		`Array.isArray([])`:                    "boolean",
		`Array.from([1])`:                      "list of integer",
		`Array.from({ length: 2 }, () => 'a')`: "list of string",
		`Promise.all([1])`:                     "list of integer",
		`structuredClone({ a: 1 })`:            "record{a}",
		`new Map()`:                            "record{}",

		// records, functions and classes
		`({ ...{ a: 1 }, b: 2 })`:                             "record{a,b}",
		`({ a: 1, b() { return 2 } }).b()`:                    "integer",
		`(async () => 1)()`:                                   "integer",
		`(function () { return 'a' })()`:                      "string",
		`new (class { get x() { return 1 } })().x`:            "integer",
		`new (class { y() { return 'a' } })().y()`:            "string",
		`new (class { constructor() { this.z = true } })().z`: "boolean",
		`Promise.resolve(1).then((n) => 'x')`:                 "string",
		`Promise.resolve(1).catch(() => null)`:                "integer",
		`((f) => f.call(null, 2))((n) => n)`:                  "integer",

		// names given values later in a function
		`(() => { const a = []; a.push('x'); return a })()`:                         "list of string",
		`(() => { const o = {}; o.a = 1; return o })()`:                             "record{a}",
		`(() => { const { a } = { a: 1 }; return a })()`:                            "integer",
		`(() => { const [a] = [1]; return a })()`:                                   "integer",
		`(() => { let out = null; for (const n of [1]) { out = n } return out })()`: "integer?",
		`(() => { function f() { return 'a' } return f() })()`:                      "string",
		`((a = 3) => a)()`: "integer",

		// a 0 written in the source is a number when nothing else says what the name holds
		`(() => { let x = 0; x = unknownCall(); return x })()`: "number",

		// totals and lists built up a step at a time
		`(() => { let t = 0; t += 1.5; return t })()`:                                      "number",
		`(() => { let t = 0; t += 1; return t })()`:                                        "integer",
		`(() => { let s = ''; s += 'a'; return s })()`:                                     "string",
		`(() => { const o = { t: 0 }; o.t += 1.5; return o })().t`:                         "number",
		`(() => { const o = { t: 0 }; [1.5].forEach((n) => { o.t += n }); return o })().t`: "number",
		`(() => { const o = { t: 0 }; [1].forEach((o) => { o.t += 1.5 }); return o })().t`: "integer",
		`(() => { const a = []; [1].forEach((n) => { a.push('x') }); return a })()`:        "list of string",
		`(() => { const a = unknownCall(); a.push(1); return a })()`:                       "list of integer",
		`(() => { const a = []; a.push(1); return a })()`:                                  "list of integer",
		`(() => { let a = [1]; a = a.map((n) => 'x'); return a })()`:                       "list of string",
		`(() => { let a = [1]; a = unknownCall(a); return a })()`:                          "list of integer",
		`unknownCall().length`: "integer",
		`new (class { constructor() { this.t = 0 } add(n) { this.t += parseFloat(n) } })().t`: "number",
	}
	for src, want := range cases {
		if got := shape(evalJS(t, "module.exports = "+src)); got != want {
			t.Errorf("%s: %s, want %s", src, got, want)
		}
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()

	a := (&jsType{kind: objectKind, name: "A"}).withField("x", str)
	b := (&jsType{kind: objectKind, name: "B"}).withField("y", integer)

	cases := []struct {
		name     string
		got      *jsType
		kind     kind
		nullable bool
	}{
		{"unknown gives way", merge(unknown, str), stringKind, false},
		{"null makes it nullable", merge(null, integer), integerKind, true},
		{"whole and fractional numbers are numbers", merge(integer, num), numberKind, false},
		{"text and a number do not merge", merge(str, integer), mixedKind, false},
		{"two records merge", merge(a, b), objectKind, false},
		{"lists merge their items", merge(listOf(integer), listOf(num)), arrayKind, false},
	}
	for _, c := range cases {
		if c.got.kind != c.kind || c.got.nullable != c.nullable {
			t.Errorf("%s: kind %d nullable %v, want %d %v", c.name, c.got.kind, c.got.nullable, c.kind, c.nullable)
		}
	}

	ab := merge(a, b)
	if ab.name != "" || ab.field("x") == nil || ab.field("y") == nil {
		t.Errorf("two records with different names: %+v, want both fields and no name", ab)
	}
	if aa := merge(a, a.withField("z", boolean)); aa.name != "A" || aa.field("z") == nil {
		t.Errorf("one record twice: %+v, want its name and every field", aa)
	}
	if l := merge(listOf(integer), listOf(num)); l.elem.kind != numberKind {
		t.Errorf("list items: %d, want number", l.elem.kind)
	}
}

func TestJSONName(t *testing.T) {
	t.Parallel()

	cases := map[[2]string]string{
		{"LibraryItem", "toOldJSON"}:         "LibraryItem",
		{"LibraryItem", "toOldJSONExpanded"}: "LibraryItemExpanded",
		{"Feed", "toJSONMinified"}:           "FeedMinified",
		{"Book", "oldMetadataToJSON"}:        "BookMetadata",
		{"Book", "getAbsMetadataJson"}:       "",
		{"Book", "save"}:                     "",
	}
	for in, want := range cases {
		if got := jsonName(in[0], in[1]); got != want {
			t.Errorf("jsonName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
