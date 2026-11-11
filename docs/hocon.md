# The HOCON form

The same gateway can be written in [HOCON](https://github.com/lightbend/config/blob/main/HOCON.md) (Human-Optimized
Config Object Notation, as the Lightbend specification defines it). Every key has the same name, meaning and rules as
in the YAML form, and the generated file is the same, byte for byte.

A proxyfile's file name decides its form, never its content. A file named `Proxyfile`, with a capital P, or a name
ending in `.conf` is HOCON, a name ending in `.yaml` or `.yml` is YAML, and any other name is refused.

```hocon
# The smallest proxyfile, in HOCON: one service, one route, and the browser origins it admits.
listen = 8000
services {
  app { http = 3000 }
}
origins {
  all { hosts = [localhost], origins = ["http://localhost:3000"] }
}
routes = [
  { name = app, prefix = /, to = app }
]
```

[`examples/full/gateway.conf`](../examples/full/gateway.conf) writes every key but `services.<name>.address` and
`tracing` and sits beside [`examples/full/gateway.yaml`](../examples/full/gateway.yaml), which gives the same file.
[`examples/tracing/gateway.conf`](../examples/tracing/gateway.conf) writes `tracing` and sits beside its YAML twin,
[`examples/tracing/gateway.yaml`](../examples/tracing/gateway.yaml).
[`examples/partial/gateway.conf`](../examples/partial/gateway.conf) pulls its methods in with
`include required("rpc-methods.conf")`: a plain `include` is skipped when the file is missing, and `required` refuses it
by name.

The rules of the form:

- **Routes are an array.** `routes = [ ... ]` lists the routes in the order they are tried, and the rpc entry is the
  item `{ rpc { ... } }`, where YAML writes `- rpc:`. It is an array, not an object, because the first route that
  matches wins and HOCON does not require a reader to keep the order of an object's keys.
- **Quote what HOCON would read differently.** Quote a key that holds `.` or `*`, as in `"example.shop.*" = shop` and
  `"*" = web`: an unquoted dotted key is a path (`a.b = 1` means `a { b = 1 }`), and `*` is a syntax error outside
  quotes. Quote any value that holds anything but letters, digits, spaces and `.`, `-`, `_`, `/` and `%` (a
  substitution, below, is the one exception). HOCON refuses most other characters outside quotes, such as `*`, `:`,
  `$`, `^`, `+`, `?`, `=` and `[`, with a syntax error that does not say to quote, except the one for `+`, which says
  `outside quotes`. HOCON starts a comment at `#` and at `//`, so `rewrite = /a#b` is read as `/a`, and proxyfile
  cannot see the cut.
- **A string is text, and a number or a boolean is not.** So `max_age = "600"`, which needs text, and `listen = 8000`,
  which needs a number. A word written without quotes, such as `allow`, is a string. A quoted number where a number
  belongs, and a plain number where text belongs, are both refused.
- **A key written twice keeps the later value.** Two objects under one key merge, and an array written twice keeps the
  later array: a second `routes = [ ... ]` replaces the first. Two routes with the same name are a problem in both
  forms. The YAML form refuses a key written twice. HOCON merges a service or an environment written twice and does
  not report it.
- **Includes are relative to the file that holds them**, not to the working directory. A relative name may climb out of
  that directory, as `include "../shared/origins.conf"` does, and an absolute name is read as written, both as HOCON
  defines. `include "other.conf"` skips a file that is missing, as HOCON defines, and `include required("other.conf")`
  refuses it by name. Use `required` for a file the gateway cannot do without. `include file("other.conf")` is
  refused, because HOCON reads a name inside `file(...)` from the working directory, so the same proxyfile would read
  another file from another directory; write `include "other.conf"`, which is read beside the file that holds it.
- **Substitutions read the files, never the environment.** `${name}` takes another key of the same file or of its
  includes. It never reads an environment variable, so the same files give the same bytes on every machine, and
  `listen = ${HOME}` is a problem. The key a substitution reads is a key of the format like any other, so a
  substitution copies one proxyfile value into another, such as `accept = ${auth.send}`. A key of your own, such as
  `port = 3000` for `http = ${port}`, is refused as an unknown key:
  `gateway.conf: port: unknown key "port"; known here: admin, auth, cors, ...`.
- **The order you write is kept.** The generated file lists the services, their ports and the environments as written.
  When an object is written twice, the merged object lists the keys of the later one first.
- **An empty file is an empty object**, so the problems name each required key.

What a problem looks like:

- A syntax error is `<file>:<line>:<column>: <message>`, with a column, which a YAML syntax error does not have, for
  example `gateway.conf:3:1: unexpected EOF, expected '}'`.
- Any later problem is `<file>: <key path>: <message>`, because the HOCON reader keeps no line for a value, for example
  `gateway.conf: services.app.http: "http" must be a whole number from 1 to 65535, not quoted or block text`. The key
  path joins the keys from the top with dots (`services.app.http`), writes an array item as its index from 0 in square
  brackets (`routes[1].to`), and quotes a key that holds anything but letters, digits, `-` and `_`
  (`routes[1].rpc.to."example.shop.*"`). A problem at the top of the file, such as a missing required key, has no key
  path: `gateway.conf: "listen" is required here`.
- A message that names another place says `at <key path>` where the YAML form says `on line 7`, for example
  `gateway.conf: routes[1].name: route "app" is declared twice (the first is at routes[0].name)`.
- A file that cannot be read is `<file>: the file cannot be read: <reason>`, the same text in both forms. An included
  file is the exception, below.
- A problem inside an included file is reported against the file you gave. A syntax error there reads
  `<file>: parse error at line <n>, col <m>: <message>`, for example
  `gateway.conf: parse error at line 3, col 1: unexpected EOF, expected '}'`. It has no `:<line>:<column>:` after the
  file, and the line and column are the included file's, because the HOCON library does not say which file it was. A
  later problem names the key path the included file wrote, for example
  `gateway.conf: rpc.methods[0]: the report "x,y" of a.v1.S/M contains a comma, the separator of x-gateway-reports`. A
  missing `include required(...)` reads `<included file>: cannot read include file: <reason>`.
- The refusal of `include file(...)` names the file that holds the statement, with its line and column, also when
  that is an included file, for example
  `a.conf:1:1: include file(...) is read from the working directory, not beside this file; write include required("p.conf")`.
- The ending `, not quoted or block text` follows a message about a whole number or `true` or `false` that was written
  as text. It is the YAML form's wording; in HOCON it means a string, and an unquoted word is one too, so `http = abc`
  gets it.

[Editors](editors.md#editor-support) says how an editor and GitHub highlight a HOCON proxyfile.
