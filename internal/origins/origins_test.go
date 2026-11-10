package origins

import (
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/testsupport"
	"github.com/kitecorp/proxyfile/internal/tree"
)

func TestPatternRegex(t *testing.T) {
	cases := []struct{ pattern, want string }{
		{"https://*.example.com", `^https://.*\.example\.com$`},
		{"http://localhost:*", `^http://localhost:.*$`},
		// Of these the rules admit only the dot, but each has a meaning in RE2, so each is quoted.
		{`a\.+?()[]{}|^$b`, `^a\\\.\+\?\(\)\[\]\{\}\|\^\$b$`},
	}
	for _, c := range cases {
		if got := PatternRegex(c.pattern); got != c.want {
			t.Errorf("PatternRegex(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// Spring's breadth, kept on purpose: a * crosses dots, the dot before the domain is needed, and no port follows.
func TestPatternMeaning(t *testing.T) {
	corpus := []string{"https://shop.example.com", "https://a.b.example.com", "https://x.test.example.com",
		"https://example.com", "https://shopexample.com", "https://shop.example.com:8443", "http://shop.example.com",
		"https://shop.example.com.example.net"}
	pattern := regexp.MustCompile(PatternRegex("https://*.example.com"))
	var got []string
	for _, origin := range corpus {
		if pattern.MatchString(origin) {
			got = append(got, origin)
		}
	}
	want := []string{"https://shop.example.com", "https://a.b.example.com", "https://x.test.example.com"}
	if !slices.Equal(got, want) {
		t.Errorf("origins matched = %q, want %q", got, want)
	}
}

func TestSameOriginRegex(t *testing.T) {
	if got, want := SameOriginRegex("api.example.com"), `^https?://api\.example\.com(:[0-9]+)?$`; got != want {
		t.Errorf("SameOriginRegex(%q) = %q, want %q", "api.example.com", got, want)
	}
}

// everyRule breaks each rule of an environment, the order rule on the origins only, apart from a name listed twice.
var everyRule = testsupport.Document{
	`{`,
	`  "environments": [`,
	`    {"name": "live",`,
	`     "hosts": ["api.example.com"],`,
	`     "origins": ["https://example.com/", "https://Example.org"],`,
	`     "patterns": ["https://example.net"]},`,
	`    {"name": "test",`,
	`     "hosts": ["a_b", "api.example.com"]}`,
	`  ]`,
	`}`,
}

func TestReadProblems(t *testing.T) {
	cases := []struct {
		name, file string
		// absent writes nothing, so the file does not exist.
		absent bool
		want   []string
	}{
		{name: "no environment", file: `{"environments": []}`,
			want: []string{`o.json:1:18: the file names no environment`}},
		{name: "unknown key", file: `{"environments": [{"name": "a", "hosts": ["x.example"], "domains": []}]}`,
			want: []string{`o.json:1:57: unknown key "domains"; known here: hosts, name, origins, patterns`}},
		{name: "name twice", file: `{"environments": [{"name": "a", "hosts": ["x.example"]}, {"name": "a", "hosts": ["y.example"]}]}`,
			want: []string{`o.json:1:58: environment "a" is listed twice`}},
		// Both spellings of no name, counted from 1; two environments without a name are not also listed twice.
		{name: "name empty or null", file: `{"environments": [{"name": "", "hosts": ["x.example"]}, {"name": null, "hosts": ["y.example"]}]}`,
			want: []string{`o.json:1:28: environment 1: the name is empty`, `o.json:1:66: environment 2: the name is empty`}},
		{name: "hosts empty", file: `{"environments": [{"name": "a", "hosts": []}]}`,
			want: []string{`o.json:1:42: environment "a": hosts is empty`}},
		{name: "every rule", file: everyRule.String(), want: []string{
			`o.json:5:17: environment "live": the origins are not sorted, or list one twice`,
			`o.json:5:18: environment "live": the origin "https://example.com/" is not scheme://host[:port] in lower case`,
			`o.json:5:42: environment "live": the origin "https://Example.org" is not scheme://host[:port] in lower case`,
			`o.json:6:19: environment "live": the pattern "https://example.net" is not scheme://host[:port] with a *`,
			`o.json:8:16: environment "test": the host "a_b" is not a lower-case host name without a port`,
			`o.json:8:23: environment "test": the host "api.example.com" is in two environments`,
		}},
		{name: "hosts not sorted", file: `{"environments": [{"name": "a", "hosts": ["y.example", "x.example"]}]}`,
			want: []string{`o.json:1:42: environment "a": the hosts are not sorted, or list one twice`}},
		// A host listed twice in one environment is in one environment, so only the order rule names it.
		{name: "host twice in one environment", file: `{"environments": [{"name": "a", "hosts": ["x.example", "x.example"]}]}`,
			want: []string{`o.json:1:42: environment "a": the hosts are not sorted, or list one twice`}},
		{name: "pattern listed twice",
			file: `{"environments": [{"name": "a", "hosts": ["x.example"], "patterns": ["https://*.example.com", "https://*.example.com"]}]}`,
			want: []string{`o.json:1:69: environment "a": the patterns are not sorted, or list one twice`}},
		{name: "pattern with a * in the wrong form",
			file: `{"environments": [{"name": "a", "hosts": ["x.example"], "patterns": ["https://*.Example.com"]}]}`,
			want: []string{`o.json:1:70: environment "a": the pattern "https://*.Example.com" is not scheme://host[:port] with a *`}},
		{name: "file missing", absent: true,
			want: []string{`o.json: the file cannot be read: no such file or directory`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "o.json")
			if !c.absent {
				testsupport.WriteFile(t, path, c.file)
			}
			file, problems := Read(path)
			if got := testsupport.ProblemStringsIn(dir, problems); !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
			if file != nil {
				t.Errorf("file = %#v, want nil", file)
			}
		})
	}
}

// The environments keep the order written, which is not sorted in the first row, and a key that was not written is
// empty.
func TestRead(t *testing.T) {
	cases := []struct {
		name, file string
		// want leaves out the file of each position; the test puts the temporary path in.
		want File
	}{
		{name: "every key", file: testsupport.Document{
			`{`,
			`  "cors": {"allow_methods": "GET,POST", "allow_headers": "authorization", "expose_headers": "x-request-id", "max_age": "600"},`,
			`  "environments": [`,
			`    {"name": "test", "hosts": ["api.example.com", "example.com"], "origins": ["https://example.com", "https://www.example.com"],`,
			`     "patterns": ["http://localhost:*", "https://*.example.com"]},`,
			`    {"name": "live", "hosts": ["api.example.org"]}`,
			`  ]`,
			`}`,
		}.String(), want: File{
			CORS: CORS{AllowMethods: "GET,POST", AllowHeaders: "authorization", ExposeHeaders: "x-request-id", MaxAge: "600"},
			Environments: []Environment{
				{Name: "test", Hosts: []string{"api.example.com", "example.com"},
					Origins:  []string{"https://example.com", "https://www.example.com"},
					Patterns: []string{"http://localhost:*", "https://*.example.com"}, Pos: model.Pos{Line: 4, Column: 5}},
				{Name: "live", Hosts: []string{"api.example.org"}, Pos: model.Pos{Line: 6, Column: 5}},
			},
		}},
		{name: "no cors", file: `{"environments": [{"name": "only", "hosts": ["example.com"]}]}`, want: File{
			Environments: []Environment{{Name: "only", Hosts: []string{"example.com"}, Pos: model.Pos{Line: 1, Column: 19}}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "o.json")
			testsupport.WriteFile(t, path, c.file)
			got, problems := Read(path)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			for i := range c.want.Environments {
				c.want.Environments[i].Pos.File = path
			}
			if !reflect.DeepEqual(got, &c.want) {
				t.Errorf("Read() = %#v, want %#v", got, &c.want)
			}
		})
	}
}

// The environments written in the proxyfile are each named by their key and placed at it, keep the order written,
// and follow the rules of an origins file. The first row is TestRead's first row written in the proxyfile.
func TestInline(t *testing.T) {
	cases := []struct {
		name, document string
		want           *File // nil when the row expects problems
		problems       []string
	}{
		{name: "every key", document: testsupport.Document{
			`cors: {allow_methods: "GET,POST", allow_headers: authorization, expose_headers: x-request-id, max_age: "600"}`,
			`origins:`,
			`  test: {hosts: [api.example.com, example.com], origins: ["https://example.com", "https://www.example.com"], patterns: ["http://localhost:*", "https://*.example.com"]}`,
			`  live: {hosts: [api.example.org]}`,
		}.String(), want: &File{
			CORS: CORS{AllowMethods: "GET,POST", AllowHeaders: "authorization", ExposeHeaders: "x-request-id", MaxAge: "600"},
			Environments: []Environment{
				{Name: "test", Hosts: []string{"api.example.com", "example.com"},
					Origins:  []string{"https://example.com", "https://www.example.com"},
					Patterns: []string{"http://localhost:*", "https://*.example.com"}, Pos: model.Pos{File: "g.yaml", Line: 3, Column: 3}},
				{Name: "live", Hosts: []string{"api.example.org"}, Pos: model.Pos{File: "g.yaml", Line: 4, Column: 3}},
			},
		}},
		{name: "no environment", document: "origins: {}\n",
			problems: []string{`g.yaml:1:10: "origins" names no environment`}},
		{name: "host in two environments", document: "origins: {a: {hosts: [x.example]}, b: {hosts: [x.example]}}\n",
			problems: []string{`g.yaml:1:48: environment "b": the host "x.example" is in two environments`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, problems := tree.FromYAML("g.yaml", []byte(c.document))
			if problems != nil {
				t.Fatalf("FromYAML problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			got, problems := Inline(root.Get("cors"), root.Get("origins"))
			if gotProblems := testsupport.ProblemStrings(problems); !slices.Equal(gotProblems, c.problems) {
				t.Errorf("problems = %q, want %q", gotProblems, c.problems)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Inline() = %#v, want %#v", got, c.want)
			}
		})
	}
}
