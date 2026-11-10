package envoy

import (
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/origins"
)

// A value from the input comes out as written, whatever it holds. An environment's name can be any text: an origins
// file gives it no pattern, and the proxyfile asks only for one character or more. One that reads like a place to
// splice stays text: q writes it in quotes, and only a plain $name is replaced. One with a line break stays in q's
// quotes too: as a literal block, a first line that starts with a tab would not read back.
func TestInputTextComesOutAsWritten(t *testing.T) {
	cases := []struct{ name, environment string }{
		{"environment named like the routes' place", "$routes"},
		{"environment name starting with a tab and holding a line break", "\tlive\nsite"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := model.Service{Name: "app", Address: model.Text{Value: "127.0.0.1"},
				Ports: []model.ServicePort{{Protocol: model.HTTP, Number: 3000}}}
			in := Input{
				Gateway: &model.Gateway{Namespace: "proxyfile", Listen: model.Port{Number: 8000}, Services: []model.Service{app},
					Routes: []model.Route{{Name: model.Text{Value: "app"}, Match: model.Prefix, Value: model.Text{Value: "/"},
						To: model.Text{Value: "app"}}}},
				Origins: &origins.File{Environments: []origins.Environment{{Name: c.environment, Hosts: []string{"localhost"}}}},
			}
			out, err := Generate(in)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			var file struct {
				StaticResources struct {
					Listeners []struct {
						FilterChains []struct {
							Filters []struct {
								TypedConfig struct {
									RouteConfig struct {
										VirtualHosts []struct{ Name any } `yaml:"virtual_hosts"`
									} `yaml:"route_config"`
								} `yaml:"typed_config"`
							}
						} `yaml:"filter_chains"`
					}
				} `yaml:"static_resources"`
			}
			if err := yaml.Unmarshal(out, &file); err != nil {
				t.Fatalf("reading the generated file: %v", err)
			}
			got := file.StaticResources.Listeners[0].FilterChains[0].Filters[0].TypedConfig.RouteConfig.VirtualHosts[0].Name
			if got != c.environment {
				t.Errorf("virtual host name = %#v, want %q", got, c.environment)
			}
		})
	}
}

// Every text q writes into a fragment reads back from YAML as the same text, so no value an earlier stage accepted
// can make Generate fail; the characters go-yaml will not read inside quotes are written as escapes.
func TestScalarReadsBack(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"plain", "/api", `"/api"`},
		{"quote and backslash", `a"b\c`, `"a\"b\\c"`},
		{"C0 controls", "a\tb\x01", `"a\tb\u0001"`},
		{"delete", "a\x7fb", `"a\u007fb"`},
		{"next line, a line break to go-yaml", "a\u0085b", `"a\u0085b"`},
		{"other C1 controls", "\u0080\u009f", `"\u0080\u009f"`},
		{"line and paragraph separators", "\u2028\u2029", `"\u2028\u2029"`},
		{"the two noncharacters go-yaml refuses", "\ufffe\uffff", `"\ufffe\uffff"`},
		{"HTML characters", "<a&b>", `"<a&b>"`},
		{"other characters as they are", "é€\U0001F600", "\"é€\U0001F600\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scalar(c.text)
			if got != c.want {
				t.Errorf("scalar(%q) = %s, want %s", c.text, got, c.want)
			}
			var back struct{ V string }
			if err := yaml.Unmarshal([]byte("v: "+got), &back); err != nil {
				t.Fatalf("reading %s back: %v", got, err)
			}
			if back.V != c.text {
				t.Errorf("%s reads back as %q, want %q", got, back.V, c.text)
			}
		})
	}
}

// The manifest is text in the tree, so its layout is fixed: the default rule on the second line, each rule after the
// first on its own line under the first's {, and a rate as the shortest decimal of the percentage over 100.
func TestManifest(t *testing.T) {
	cases := []struct {
		name   string
		sample model.Sample
		want   string
	}{
		{name: "no rule", sample: model.Sample{Default: model.SampleRule{First: 1, RatePercent: 5}},
			want: "{\"version\": 2,\n \"default\": {\"fixed_target\": 1, \"rate\": 0.05}}\n"},
		{name: "the bounds", sample: model.Sample{Default: model.SampleRule{First: 4294967295, RatePercent: 100},
			Rules: []model.SampleRule{{Host: "*", Method: "*", Path: "/live*", First: 0, RatePercent: 0}}},
			want: "{\"version\": 2,\n \"default\": {\"fixed_target\": 4294967295, \"rate\": 1},\n" +
				" \"rules\": [{\"host\": \"*\", \"http_method\": \"*\", \"url_path\": \"/live*\", \"fixed_target\": 0, \"rate\": 0}]}\n"},
		{name: "two rules, and a path with a quote", sample: model.Sample{Default: model.SampleRule{First: 3, RatePercent: 50},
			Rules: []model.SampleRule{
				{Host: "api.example.com", Method: "GET", Path: `/a"b`, First: 1, RatePercent: 10},
				{Host: "*", Method: "P?T", Path: "*", First: 2, RatePercent: 1},
			}},
			want: "{\"version\": 2,\n \"default\": {\"fixed_target\": 3, \"rate\": 0.5},\n" +
				" \"rules\": [{\"host\": \"api.example.com\", \"http_method\": \"GET\", \"url_path\": \"/a\\\"b\", \"fixed_target\": 1, \"rate\": 0.1},\n" +
				"           {\"host\": \"*\", \"http_method\": \"P?T\", \"url_path\": \"*\", \"fixed_target\": 2, \"rate\": 0.01}]}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := manifest(&c.sample); got != c.want {
				t.Errorf("manifest =\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}
