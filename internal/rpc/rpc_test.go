package rpc

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/read"
	"github.com/kitecorp/proxyfile/internal/testsupport"
)

// longest maps one package each to four services, so each method shows which of the patterns that match it wins.
var longest = testsupport.Document{
	"listen: 8000",
	"services:",
	"  a: {grpc: 5001}",
	"  b: {grpc: 5002}",
	"  c: {grpc: 5003}",
	"  d: {grpc: 5004}",
	"origins: origins.json",
	"routes:",
	"  - rpc:",
	"      to:",
	"        example.*: a",
	"        example.shop.*: b",
	"        example.shop.v1.CartService: c",
	`        "*": d`,
}

// single is longest with one service and one pattern, so a method outside the pattern's package has no target.
var single = testsupport.Document{
	"listen: 8000",
	"services:",
	"  shop: {grpc: 5001}",
	"origins: origins.json",
	"routes:",
	"  - rpc:",
	"      to:",
	"        example.shop.*: shop",
}

// tie writes a pattern and a full service name of the same length, once in each order, so neither the written order
// nor the length can pick the full name for both methods.
var tie = testsupport.Document{
	"listen: 8000",
	"services:",
	"  p: {grpc: 5001}",
	"  e: {grpc: 5002}",
	"origins: origins.json",
	"routes:",
	"  - rpc:",
	"      to:",
	"        a.b.*: p",
	"        a.b.C: e",
	"        c.d.E: e",
	"        c.d.*: p",
}

// many breaks each rule of a method once, one method per line.
var many = testsupport.Document{
	`[`,
	`  {"service": "example.other.v1.X", "method": "Y"},`,
	`  {"service": "example.shop.v1.C", "method": "M", "reports": ["a,b"]},`,
	`  {"service": "example.shop.v1.C", "method": "M"}`,
	`]`,
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		// example is a proxyfile read in place; when it is empty, proxyfile is written to g.yaml in a temporary
		// directory.
		example, proxyfile string
		methods            string // written to m.json in the temporary directory
		// want is each route as its path, cluster and name, then each problem; a row expects routes or problems.
		want []string
	}{
		{name: "sorted, clustered, named", example: "../../examples/full/gateway.yaml",
			methods: `[{"service": "example.shop.v1.CartService", "method": "GetCart", "action": "browse"}, {"service": "example.accounts.v1.ProfileService", "method": "GetProfile"}, {"service": "example.shop.v1.CartService", "method": "AddItem", "action": "buy", "reports": ["browse"]}]`,
			want: []string{
				"/example.accounts.v1.ProfileService/GetProfile accounts_grpc accounts-rpc",
				"/example.shop.v1.CartService/AddItem shop_grpc shop",
				"/example.shop.v1.CartService/GetCart shop_grpc shop",
			}},
		{name: "longest pattern wins", proxyfile: longest.String(),
			methods: `[{"service": "example.shop.v1.CartService", "method": "X"}, {"service": "example.shop.v2.Y", "method": "Z"}, {"service": "example.other.v1.W", "method": "V"}, {"service": "other.v1.U", "method": "T"}]`,
			want: []string{
				"/example.other.v1.W/V a_grpc a-rpc",
				"/example.shop.v1.CartService/X c_grpc c-rpc",
				"/example.shop.v2.Y/Z b_grpc b-rpc",
				"/other.v1.U/T d_grpc d-rpc",
			}},
		// The routes sort by the whole path, where a dot sorts before a slash: /example.B.C/M comes first, though
		// example.B sorts before example.B.C, so a sort by service and then method would keep the written order.
		{name: "sorted by the whole path, not by service and method", proxyfile: longest.String(),
			methods: `[{"service": "example.B", "method": "M"}, {"service": "example.B.C", "method": "M"}]`,
			want:    []string{"/example.B.C/M a_grpc a-rpc", "/example.B/M a_grpc a-rpc"}},
		// example.shop.* covers the package example.shop and the packages under it, not every name that starts with
		// example.shop.
		{name: "a pattern matches whole package names", proxyfile: longest.String(),
			methods: `[{"service": "example.shopping.v1.S", "method": "M"}]`,
			want:    []string{"/example.shopping.v1.S/M a_grpc a-rpc"}},
		{name: "a full name beats a pattern of its length", proxyfile: tie.String(),
			methods: `[{"service": "a.b.C", "method": "M"}, {"service": "c.d.E", "method": "M"}, {"service": "a.b.D", "method": "M"}]`,
			want:    []string{"/a.b.C/M e_grpc e-rpc", "/a.b.D/M p_grpc p-rpc", "/c.d.E/M e_grpc e-rpc"}},
		{name: "problems", proxyfile: single.String(), methods: many.String(), want: []string{
			"m.json:2:3: no rpc mapping matches example.other.v1.X",
			`m.json:3:3: the report "a,b" of example.shop.v1.C/M contains a comma, the separator of x-gateway-reports`,
			"m.json:4:3: the method example.shop.v1.C/M is listed twice (first on line 3)",
		}},
		{name: "no rpc entry", example: "../../examples/minimal/gateway.yaml", methods: `[]`,
			want: []string{"../../examples/minimal/gateway.yaml:7:1: RPC methods are given, but the routes have no rpc entry"}},
		// Without an rpc entry no method has a target, but the rules of the methods themselves still run, so one run
		// names every problem.
		{name: "no rpc entry, the methods still checked", example: "../../examples/minimal/gateway.yaml",
			methods: `[{"service": "a.B", "method": "M", "reports": ["x,y"]}]`,
			want: []string{
				"../../examples/minimal/gateway.yaml:7:1: RPC methods are given, but the routes have no rpc entry",
				`m.json:1:2: the report "x,y" of a.B/M contains a comma, the separator of x-gateway-reports`,
			}},
		{name: "methods file keys", example: "../../examples/full/gateway.yaml", methods: `[{"service": "a.B", "methd": "M"}]`,
			want: []string{
				`m.json:1:2: "method" is required here`,
				`m.json:1:21: unknown key "methd"; known here: action, method, reports, service`,
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			proxyfile := c.example
			if proxyfile == "" {
				proxyfile = filepath.Join(dir, "g.yaml")
				testsupport.WriteFile(t, proxyfile, c.proxyfile)
			}
			g, problems := read.Proxyfile(proxyfile)
			if problems != nil {
				t.Fatalf("read.Proxyfile problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			methodsFile := filepath.Join(dir, "m.json")
			testsupport.WriteFile(t, methodsFile, c.methods)
			methods, problems := ReadMethods(methodsFile)
			if methods != nil && problems != nil {
				t.Errorf("ReadMethods returned methods %v next to problems", methods)
			}
			var routes []Route
			if problems == nil {
				routes, problems = Resolve(g, methods)
			}
			var got []string
			for _, route := range routes {
				got = append(got, route.Path()+" "+route.Cluster+" "+route.Name)
			}
			got = append(got, testsupport.ProblemStringsIn(dir, problems)...)
			if !slices.Equal(got, c.want) {
				t.Errorf("routes and problems = %q, want %q", got, c.want)
			}
		})
	}
}

// The generator reads every field of a route, so each must arrive: the method's action, reports and position, and
// the service it goes to. A key left out of the methods file is empty.
func TestResolveKeepsTheMethod(t *testing.T) {
	g, problems := read.Proxyfile("../../examples/full/gateway.yaml")
	if problems != nil {
		t.Fatalf("read.Proxyfile problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	path := filepath.Join(t.TempDir(), "m.json")
	// Neither entry starts at a column equal to its line, so a position with the two swapped cannot pass.
	testsupport.WriteFile(t, path, testsupport.Document{
		`[`,
		`  {"service": "example.shop.v1.CartService", "method": "AddItem", "action": "buy", "reports": ["browse", "view"]},`,
		`    {"service": "example.accounts.v1.ProfileService", "method": "GetProfile"}`,
		`]`,
	}.String())
	methods, problems := ReadMethods(path)
	if problems != nil {
		t.Fatalf("ReadMethods problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	routes, problems := Resolve(g, methods)
	if problems != nil {
		t.Fatalf("Resolve problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	want := []Route{
		{Method: Method{Service: "example.accounts.v1.ProfileService", Method: "GetProfile", Pos: model.Pos{File: path, Line: 3, Column: 5}},
			Target: g.Service("accounts"), Cluster: "accounts_grpc", Name: "accounts-rpc"},
		{Method: Method{Service: "example.shop.v1.CartService", Method: "AddItem", Action: "buy", Reports: []string{"browse", "view"},
			Pos: model.Pos{File: path, Line: 2, Column: 3}},
			Target: g.Service("shop"), Cluster: "shop_grpc", Name: "shop"},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Errorf("routes = %+v, want %+v", routes, want)
	}
}

func TestTable(t *testing.T) {
	cases := []struct {
		name   string
		routes []Route
		want   string
	}{
		{"no route", nil, "[]\n"},
		{"one object per line", []Route{
			{Method: Method{Service: "example.accounts.v1.ProfileService", Method: "GetProfile"}, Cluster: "accounts_grpc"},
			{Method: Method{Service: "example.shop.v1.CartService", Method: "AddItem", Action: "buy",
				Reports: []string{"browse", "view"}}, Cluster: "shop_grpc"},
		}, testsupport.Document{
			`[`,
			`  {"service": "example.accounts.v1.ProfileService", "method": "GetProfile", "action": "", "reports": [], "cluster": "accounts_grpc"},`,
			`  {"service": "example.shop.v1.CartService", "method": "AddItem", "action": "buy", "reports": ["browse", "view"], "cluster": "shop_grpc"}`,
			`]`,
		}.String()},
		// A quote and a backslash are escaped; <, > and & are written as they are, as a Jackson writer writes them.
		{"quotes, backslashes and HTML characters", []Route{
			{Method: Method{Service: "a.B", Method: "M", Action: `<a & "b">\`, Reports: []string{"x<y"}}, Cluster: "a_grpc"},
		}, testsupport.Document{
			`[`,
			`  {"service": "a.B", "method": "M", "action": "<a & \"b\">\\", "reports": ["x<y"], "cluster": "a_grpc"}`,
			`]`,
		}.String()},
	}
	for _, c := range cases {
		if got := string(Table(c.routes)); got != c.want {
			t.Errorf("%s: Table() = %q, want %q", c.name, got, c.want)
		}
	}
}
