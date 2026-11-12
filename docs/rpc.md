# `rpc`

`rpc` gives the RPC methods the gateway routes, written as a list in `rpc.methods` or read from `.proto` files with
`rpc.proto`, and the rpc entry of [routes](routes.md) sends them to the services.

<!-- keys: rpc -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `rpc` | The RPC methods the gateway routes; the routes' rpc entry maps them to services. | A mapping. Written here or read from `.proto` files, instead of in a methods file. Not given together with `--rpc-methods`. | core |
| `rpc.methods` | One entry for each RPC method the gateway routes, as a build writes them from the gRPC descriptors. | A list; each item is a mapping. It may be empty. Which service a method goes to is decided by the rpc entry of `routes`. Exactly one of `methods` and `proto` is written. | core |
| `rpc.methods[].service` | The full name of the method's service. | Required. A full service name such as shop.v1.CartService. | core |
| `rpc.methods[].method` | The method's name. | Required. A method name such as AddItem. | core |
| `rpc.methods[].action` | The action the check is told for this method. | Text. Sent to the check in `x-gateway-action`; empty when not written. | core |
| `rpc.methods[].reports` | The reports the check is told for this method. | A list; each item is text. Sent to the check in `x-gateway-reports`. No report contains a comma, because the list is sent as one value joined by commas. | core |
| `rpc.proto` | The RPC methods, read from the .proto files under roots: each method that carries the method option named by option. | A mapping. Compiled each time proxyfile runs. Not given with `--rpc-methods`. Exactly one of `methods` and `proto` is written. | core |
| `rpc.proto.roots` | The directories of .proto files, each an import root: relative to the proxyfile's directory, or absolute. | Required. A list; each item is text. Not empty, and no directory written twice. Each is an import root, as protoc's `--proto_path` is: a file's import path is its path under its root, written with `/`. A relative directory is read from the proxyfile's own directory, as `origins: <file>` is, also when the key is written in an included file; an absolute one as written. | core |
| `rpc.proto.option` | The full name of the method option that marks a method the gateway routes. | Required. An option's full name such as example.gateway.v1.route. Written `<package>.<name>`, with no leading dot. | core |
<!-- /keys -->

## The methods file

The methods are a list with one entry for each RPC method the gateway routes. They are the proxyfile's `rpc.methods`,
the methods `rpc.proto` reads from the `.proto` files (see [RPC methods from the protobuf
sources](#rpc-methods-from-the-protobuf-sources)), or, the 1.0 way, a JSON file that the `--rpc-methods` flag names.
Which service a method goes to is decided by the rpc entry of `routes`, not by this list. This is a methods file;
written as `rpc.methods`, it is the same list.

```json
[
  {"service": "example.shop.v1.CartService", "method": "GetCart", "action": "browse"},
  {"service": "example.accounts.v1.ProfileService", "method": "GetProfile"},
  {"service": "example.shop.v1.CartService", "method": "AddItem", "action": "buy", "reports": ["browse"]}
]
```

<!-- keys: methods file -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `service` | The full name of the method's service. | Required. A full service name such as shop.v1.CartService. | core |
| `method` | The method's name. | Required. A method name such as AddItem. | core |
| `action` | The action the check is told for this method. | Text. Sent to the check in `x-gateway-action`; empty when not written. | core |
| `reports` | The reports the check is told for this method. | A list; each item is text. Sent to the check in `x-gateway-reports`. No report contains a comma, because the list is sent as one value joined by commas. | core |
<!-- /keys -->

A method listed twice is a problem.

## RPC methods from the protobuf sources

`rpc.proto` names the directories of `.proto` files and the method option that marks a routed method. proxyfile
compiles the files in memory each time it renders or checks, so no list of methods is written anywhere. In YAML, it
reads `rpc: {proto: {roots: [proto], option: example.gateway.v1.route}}`.

The same in HOCON, as it stands in `examples/partial/proto-methods/Proxyfile`, whose root `../proto` is read from the
file's own directory and so names `examples/partial/proto`:

```hocon
rpc {
  proto {
    roots = [../proto]
    option = example.gateway.v1.route
  }
}
```

[`examples/partial/proto-methods/Proxyfile`](../examples/partial/proto-methods/Proxyfile) holds the whole gateway.

The roots hold two files, first `example/gateway/v1/route.proto`, which declares the option:

```proto
syntax = "proto3";

package example.gateway.v1;

import "google/protobuf/descriptor.proto";

// What the gateway asks the auth check before a browser reaches a method.
message Route {
  // The action the caller must hold; empty means any caller may reach the method.
  string action = 1;
  // The actions the answer reports on.
  repeated string reports = 2;
}

extend google.protobuf.MethodOptions {
  // Marks a method the gateway routes. A method without it is not routed.
  Route route = 50000;
}
```

then `example/backend/v1/reports.proto`, which uses it:

```proto
syntax = "proto3";

package example.backend.v1;

import "example/gateway/v1/route.proto";

service ReportService {
  // Routed: a browser may call it, and the check is asked for the action read.
  rpc ListReports(ListReportsRequest) returns (ListReportsResponse) {
    option (example.gateway.v1.route) = {action: "read", reports: ["export", "share"]};
  }
  // Not routed: no route option.
  rpc RebuildReports(RebuildReportsRequest) returns (RebuildReportsResponse);
}

message ListReportsRequest {}
message ListReportsResponse {}
message RebuildReportsRequest {}
message RebuildReportsResponse {}
```

The gateway then routes one method, `/example.backend.v1.ReportService/ListReports`, with the action `read` and the
reports `export` and `share`: the list `examples/partial/rpc-methods.conf` writes by hand.

What proxyfile does with the keys, in this order, each step only when the steps before it found no problem:

1. It reads the roots: every file whose name ends in `.proto`, at any depth under each root. A root may be a symbolic
   link to a directory; below a root a linked `.proto` file is read as the file it names and a linked directory is not
   walked. A root that cannot be read, that is not a directory or that holds no `.proto` file is a problem, and so are
   two roots that hold a file at the same import path, because an import of that path would always read the first
   root's file and nothing would read the other.
2. It parses every file it read. A syntax error is a problem with its file, line and column.
3. It chooses the files to compile: each file that declares a service, and every file it imports, at any depth. A file
   that declares no service and is imported by none of those is parsed but not compiled: it has no method, and
   compiling it could only fail on an import no route needs. Every import of a chosen file names a file under a root,
   or one of the `google/protobuf` files protoc ships, which proxyfile carries built in (a root's own copy of one of
   them comes first). Before it compiles, proxyfile names every import that names neither, at its import statement.
4. It compiles the chosen files in memory with `github.com/bufbuild/protocompile`. Every compile error that library
   reports is a problem with its file, line and column: an unknown type, a symbol declared twice (so a service or a
   method declared twice never reaches the routes), an option used where its type does not fit.
5. It finds the option by its full name among the compiled files. It extends `google.protobuf.MethodOptions` and holds
   one message, and that message has a field `action` that holds one string; anything else is a problem. A field
   `reports` is optional; when the message has it, it holds a list of strings. Other fields of the message are left
   alone.
6. It routes each method of each compiled service that carries the option, with the option's `action`, and its
   `reports` when the message has that field (none otherwise). A method without the option is not routed. A method
   whose option leaves `action` empty is routed with the empty action, as a methods-file entry with no `action` is.
7. It sorts the methods by path, `/<service>/<method>`, in byte order, and hands them on.

Accepted without a word: a method without the option; an option whose message has no `reports` field (then no method
reports anything); a method whose `action` is empty; an option that no method carries, which routes nothing, as
`methods: []` does; a file that declares no service and that no such file imports, even one whose imports name no
file; every field of the option's message other than `action` and `reports`.

The syntax is fixed, with no key: proxyfile reads proto2, proto3 and edition 2023 files, and a file in edition 2024 is
a compile error. It reads no descriptor set, no `buf.yaml` and nothing from the network: only the files under the
roots and the `google/protobuf` files it carries.

A problem is printed as every other problem is (see [Commands](commands.md)): a problem about a key at its line in YAML
and at its key path in HOCON, a problem in a `.proto` file at that file's line and column. A `.proto` file is named by
its root, resolved against the proxyfile's directory, joined with its import path.

| Case | Where | Message |
|---|---|---|
| `rpc` holds both `methods` and `proto`, or neither | the `rpc` key | `exactly one of methods, proto is needed here` |
| `rpc.proto` and `--rpc-methods` | the `proto` key | `rpc.proto is written here, so --rpc-methods must not be given` |
| An empty `roots` | the `roots` key | `the roots list is empty` |
| A root written twice | the second | `the root "proto" is written twice (the first on line 4)`; in HOCON `(the first at rpc.proto.roots[0])` |
| A root that cannot be read | the root | `the root examples/partial/proto cannot be read: no such file or directory`, the reason as the system words it |
| A root that is not a directory | the root | `the root examples/partial/proto is not a directory` |
| A root with no `.proto` file | the root | `the root examples/partial/proto holds no .proto file` |
| One import path under two roots | the later file | `the import path a/v1/s.proto is also under the root first/proto, which comes first in rpc.proto.roots` |
| A syntax error | its file, line and column | protocompile's text, such as `syntax error: expecting ';'` |
| An import that names no file | the import statement | `the import google/rpc/error_details.proto is under no root of rpc.proto.roots and is not a standard google/protobuf file` |
| A compile error | its file, line and column | protocompile's text, such as `method a.v1.S.M: unknown request type Missing`, or `symbol "a.v1.S.M" already defined at a/v1/s.proto:6:7`, which names the other place by its import path, as protocompile writes it |
| No compiled file defines the option | the `option` key | `no compiled file defines the option example.gateway.v1.rout; the compiled files are those under the roots that declare a service, and the files they import` |
| The name is not an option | the `option` key | `example.gateway.v1.Route is a message, not an option`, with the kind the name has |
| The option is not a method option | the `option` key | `the option o.v1.route extends google.protobuf.MessageOptions, not google.protobuf.MethodOptions` |
| The option does not hold one message | the `option` key | `the option o.v1.route holds a string, not one message`, or `holds a list of messages, not one` |
| No `action` that holds one string | the `option` key | `the option's message o.v1.Route has no field action that holds one string` |
| A `reports` that is not a list of strings | the `option` key | `the field reports of the option's message o.v1.Route is not a list of strings` |

`check` compiles the sources as `render` does, so a change to a routed method makes `check` fail until the next
`render`. Every `.proto` file read is an input, so no output path may name one.

## The RPC table

`--rpc-table` writes the routes as JSON for other programs to read, for example one that answers the check. It is a JSON
array with one object per line, sorted by path, and it ends with a newline. Each object has `service`, `method`,
`action` (empty when not written), `reports` (empty when not written) and `cluster`, the cluster the call goes to. With
no methods the table is `[]`. This is the table of the methods above:

```json
[
  {"service": "example.accounts.v1.ProfileService", "method": "GetProfile", "action": "", "reports": [], "cluster": "accounts_grpc"},
  {"service": "example.shop.v1.CartService", "method": "AddItem", "action": "buy", "reports": ["browse"], "cluster": "shop_grpc"},
  {"service": "example.shop.v1.CartService", "method": "GetCart", "action": "browse", "reports": [], "cluster": "shop_grpc"}
]
```
