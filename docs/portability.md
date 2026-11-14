# Portability

proxyfile writes Envoy's configuration and nginx's, and the format is written so that the same file can drive HAProxy
once it has a generator. nginx's generator writes routing in this version and refuses by name each key it does not
write yet (its column below). Every key is **core** or belongs to one proxy. A core key is one every proxy can
carry, at times with a difference this page names, or one a proxy refuses by name where it cannot carry it. A key that
belongs to one proxy lives in that proxy's block at the top of the file: `envoy`, `nginx` or `haproxy`. A render for
one target reads the core keys and its own block and leaves the other blocks alone, so one file can hold what each
proxy needs. The split follows the Kubernetes Gateway API, which sorts its features into core, extended and
implementation-specific ones and is implemented by Envoy, nginx and HAProxy alike.

`proxyfile render --target envoy` is the default. A target with no generator in this version is refused, and so is any
key written in the `nginx` or `haproxy` block, which take no key yet. A target that cannot carry a key the file uses
will refuse the file, naming the key and what that proxy offers instead; it never writes a quietly weaker
configuration.

## The keys, by proxy

The Envoy column is what proxyfile writes today. The nginx and HAProxy columns name what each proxy offers: nginx's
`ngx_http_auth_request_module` and `ngx_otel_module`; for HAProxy's external check, the community Lua script
`haproxy-auth-request` or an SPOE agent; HAProxy's own OpenTelemetry filter from 3.4 on, where its OpenTracing filter
is deprecated (it goes in 3.5).

| Key | Envoy | nginx | HAProxy | Class |
|---|---|---|---|---|
| `listen` | the listener | `listen` | `bind` | core |
| `admin` | the admin listener: `/ready`, `/stats`, `/server_info` | `stub_status` on its own port, other paths | the `stats` page or `monitor-uri`, other paths | core; the paths differ by proxy |
| `tls.certificate`, `tls.key` | the listener's TLS transport socket, with ALPN `h2` and `http/1.1` | `listen <port> ssl`, `ssl_certificate`, `ssl_certificate_key` | `bind :<port> ssl crt <file>`, which reads the key from the certificate file or a `.key` file beside it | core |
| `tls.clients.ca` | `validation_context.trusted_ca` with `require_client_certificate` | `ssl_client_certificate` with `ssl_verify_client on` | `verify required ca-file <ca>` | core |
| `client_address` | `use_remote_address` `false` (`forwarded`) or `true` (`connection`) | `forwarded` is `set_real_ip_from 0.0.0.0/0`, `real_ip_header X-Forwarded-For` and `real_ip_recursive off`; `connection` writes nothing | `forwarded` is `http-request set-src hdr_ip(X-Forwarded-For,-1)`; `connection` writes nothing | core |
| `limits.connections` | the overload manager | `worker_connections` | `maxconn` | core |
| `envoy.heap` | the overload manager: shrink at 90%, refuse at 98% | none | none | envoy |
| `limits.request_headers.size` | `max_request_headers_kb` | `large_client_header_buffers` | `tune.bufsize` | core |
| `limits.request_headers.count` | the header count limit | none | `tune.http.maxhdr` | core; nginx refuses it |
| `timeouts.request` | the route timeout, for the whole request | none: `proxy_read_timeout` counts the time between two reads, 15 s without the key and 5 minutes with `none` | `timeout http-request` for the headers, `timeout server` after them | core; nginx takes `none` and refuses a time, HAProxy's differs as its column says |
| `timeouts.idle` | the stream idle timeout | `proxy_read_timeout`, `client_body_timeout` | `timeout client`, `timeout server` | core |
| `timeouts.upstream_idle` | the cluster idle timeout | `keepalive_timeout` in `upstream` | `pool-purge-delay`, which closes half the idle server connections each interval; no idle timeout per connection | core; HAProxy differs as its column says |
| `services.<name>.http`, `.grpc`, `.address`, `.host_header` | clusters | `upstream` with `proxy_pass` or `grpc_pass`, `proxy_set_header Host` | `backend` with `server ... proto h2`, `http-request set-header Host` | core |
| `services.<name>.addresses`, a DNS name in `.address` | one endpoint per address in a `STATIC` cluster; for a name, a cluster whose `cluster_type` is `DnsCluster`, which looks it up every 5 seconds for its IPv4 and IPv6 addresses both (`dns_lookup_family: ALL`), each a backend | `upstream` with one `server` per address; for a name, `server <name>:<port> resolve` with a `zone`, in open source from 1.27.3 | one `server` line per address; for a name, `server-template` with `resolvers` | core |
| `services.<name>.balance` | `lb_policy` `ROUND_ROBIN`, `LEAST_REQUEST`, `RANDOM`, `RING_HASH` (`LEAST_REQUEST` compares two backends chosen at random) | round robin, `least_conn`, `random`, `hash ... consistent` | `balance roundrobin`, `leastconn`, `random`, `hash` | core |
| `services.<name>.hash.header`, `.hash.client` | the cluster's `hash_policy`: `header`, or `connection_properties` with `source_ip` | `hash $http_<name> consistent`, `hash $binary_remote_addr consistent` | `balance hdr(<name>)`, `balance source` | core |
| `services.<name>.health` | `health_checks` on the cluster, with the panic threshold at 0 | none in open source (nginx Plus's `health_check`), so it refuses the key | `option httpchk GET <path>` with `server ... check inter <interval> fall <unhealthy> rise <healthy>`; for `grpc`, an HTTP/2 check of the health RPC's path | core; nginx refuses it |
| `dns.resolver` | c-ares `resolvers` in the `DnsCluster` of each cluster that looks a name up; the host's configuration without it | `resolver`; a name without `dns.resolver` is refused | a `resolvers` section, or `parse-resolv-conf` without it | core; nginx refuses a name without it |
| `auth.ask`, `.timeout`, `.enabled`, `.send`, `.accept` | `ext_authz` in HTTP mode | `auth_request`, `proxy_set_header`, `auth_request_set` | the `haproxy-auth-request` script or an SPOE agent; nothing built in | core on Envoy and nginx; on HAProxy through the script |
| `auth.when_unavailable = allow` | `failure_mode_allow` | in part: `error_page` on the check's 5xx | the script's option | core; partial on nginx |
| `auth.only_with_header` | a header match on the filter | in part: a `map` and a second `location` | the script with an ACL | core; partial on nginx |
| `auth.accept` with a `*` prefix | allowed upstream header prefixes | each header named in `auth_request_set`; no prefix | the script copies every header | core; nginx refuses the prefix form |
| `auth.send_request` | the check's `headers_to_add` | four `proxy_set_header` lines on the auth subrequest | the auth script's forwarded headers | core |
| `auth.jwt` | the `jwt_authn` filter with one provider | none in nginx open source (nginx Plus's `auth_jwt` only), so its generator refuses the key | `jwt_verify` takes one PEM key, no key set and no URL, so its generator refuses the key until it has a faithful form | core; nginx and HAProxy refuse it |
| `remove_from_clients` | removal by name and by prefix | `proxy_set_header X ""` for each name; no prefix | `http-request del-header -m beg` | core; nginx refuses the prefix form |
| `cors.*`, `origins.<env>.origins`, `.patterns` | the CORS filter | `map $http_origin`, `add_header`, a preflight `return 204` | ACLs, `http-request return` for the preflight, `http-after-response set-header` | core |
| `origins.<env>.hosts` | virtual host domains | `server_name` | `hdr(host)` ACLs and `use_backend` | core |
| `routes[].name` | the route's name, in the logs | none beyond a log variable | the ACL's name | core; nginx keeps it in the log only |
| `routes[].prefix`, `.path` | prefix and path matches | `location ^~`, `location =` | `path_beg`, `path` | core |
| `routes[].pattern` | RE2 | PCRE, `location ~` | PCRE, `path_reg` | core, on the part of regular-expression syntax the three share |
| `routes[].method` | a header match | `limit_except` or `if ($request_method)` | the `method` ACL | core |
| `routes[].to` | the cluster | `proxy_pass` | `use_backend` | core |
| `routes[].rewrite` | a prefix rewrite | `rewrite`, or the URI on `proxy_pass` | `http-request replace-path` | core |
| `routes[].auth = off` | the check and the token filter turned off for the route | a `location` without `auth_request` | an ACL that skips the check | core |
| `routes[].retries`, `rpc.retries` | a retry policy with Envoy's conditions, to a backend not tried yet when the service has one (the `previous_hosts` predicate) | `proxy_next_upstream` and `proxy_next_upstream_tries` | `retries` and `retry-on` | core; what "never reached the service" means differs by proxy |
| `routes[].rate_limit`, `routes[].rpc.rate_limit` | the local rate limit filter, before the check, with one token bucket for each route, which the route's metadata names | a `limit_req_zone` for each route, keyed by `$binary_remote_addr`, or by a constant for `all`, with `rate=<requests>r/s` or `r/m`, so a `per` other than `1s` or `1m` is refused; `limit_req ... burst=<requests + burst - 1> nodelay` and `limit_req_status 429`, which a gRPC client reads as UNAVAILABLE and may retry | a `stick-table` storing `http_req_rate(<per>)`, `http-request track-sc0`, and `http-request deny deny_status 429 if { sc_http_req_rate(0) gt <requests> }`, a sliding window, so a `burst` above 0 is refused | core; nginx and HAProxy differ as their columns say |
| the `rpc` entry of `routes`, `rpc.to`, `rpc.methods`, `rpc.proto` | one exact-path route for each method | a `location = /<package>.<Service>/<Method>` with `grpc_pass` for each method | a `path` ACL for each method, to an HTTP/2 backend | core |
| the action and the reports each RPC route tells the check | the route's own check settings | in part: an auth `location` for each method, or a `map` | the script's variables | core; partial on nginx |
| `envoy.connect_bridge` | the Connect-gRPC bridge and the Lua status fix on RPC routes | none | none | envoy |
| `log.status_from`, `log.sample` | access log filters | `access_log ... if=` with `split_clients` | `http-request set-log-level` with a `rand()` ACL | core |
| `tracing`, with its provider `xray` | Envoy's X-Ray tracer | `ngx_otel_module`, exporting to an OpenTelemetry Collector with the AWS X-Ray exporter, whose configuration the nginx generator will also write | the OpenTelemetry filter (3.4 and later), then the same collector | core |
| `tracing`, with its provider `otel` | Envoy's OpenTelemetry tracer | `ngx_otel_module`: `otel_exporter { endpoint }`, `otel_service_name`, `otel_trace on` and `otel_trace_context propagate` | the OpenTelemetry filter (3.4 and later) | core |
| `envoy.tracing` | the same as `tracing` with `xray` | none | none | envoy, the old place of `tracing` |
| `envoy.namespace` | the namespace of the gateway's metadata | none | none | envoy |

Everything else in the format, such as the origins file form, the entries of `rpc.methods` and the `name` of an RPC
mapping, is core. The cost of another proxy is its generator and the proof that it behaves as the Envoy one does.

## Support by proxy, from the generators

Each generator declares, for every core key, how it carries the key: `supported`, `partial` with the difference, or
`refused` with what that proxy offers instead. Its last two rows are the two behaviours every configuration holds with
no key (below): each generator declares them as it declares a key, and none may refuse them. `proxyfile render` and
`proxyfile check` with `--target <proxy>` refuse a file that writes a key its target refuses, naming the key where it
is written and what the proxy offers instead. This table is written from those declarations and has a column for each
proxy with a generator; the table above stays until every proxy's column is written here. A partial cell that names a
refused form (`refuses <form>: <what to write instead>`) is refused as a refused key is: render and check name the key
where it is written. What a proxy writes differently with no key is on its own page: [nginx](nginx.md). The
[conformance battery](conformance.md) sends the same requests to each proxy, and checks the refusal for a key the proxy
refuses.

<!-- keys: support by proxy -->
| Key | Envoy | nginx |
|---|---|---|
| `listen` | supported | supported |
| `admin` | supported | partial: answers /ready alone; every other path of the admin port is 404 |
| `tls` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tls.certificate` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tls.key` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tls.clients` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tls.clients.ca` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `client_address` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `limits` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `limits.connections` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `limits.request_headers` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `limits.request_headers.size` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `limits.request_headers.count` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `timeouts` | supported | supported |
| `timeouts.request` | supported | partial: without the key nginx answers 504 to a service silent for 15 s after the request, as the Envoy output does, but lets an answer that keeps coming run past the 15 s at which Envoy ends it; with none, nginx ends a request after 5 minutes with nothing read or written, as Envoy does; refuses a time: nginx has no limit on a whole request (its time limits count the time between two reads or writes); write timeouts.request: none, or render it for envoy |
| `timeouts.idle` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `timeouts.upstream_idle` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `services` | supported | supported |
| `services.<service name>` | supported | supported |
| `services.<service name>.http` | supported | supported |
| `services.<service name>.grpc` | supported | supported |
| `services.<service name>.host_header` | supported | supported |
| `services.<service name>.address` | supported | partial: an IP address with no zone; refuses a DNS name: not written by this version's nginx generator; render it for envoy; refuses a zoned IPv6 address: nginx reads no zone in a backend's address; give the service an address without one |
| `services.<service name>.addresses` | supported | partial: IP addresses with no zone; refuses holding a zoned IPv6 address: nginx reads no zone in a backend's address; give the service an address without one |
| `services.<service name>.balance` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `services.<service name>.hash` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `services.<service name>.hash.header` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `services.<service name>.hash.client` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `services.<service name>.health` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `services.<service name>.health.path` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `services.<service name>.health.grpc` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `services.<service name>.health.interval` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `services.<service name>.health.timeout` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `services.<service name>.health.healthy` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `services.<service name>.health.unhealthy` | supported | refused: nginx open source has no active health checks (nginx Plus's health\_check); remove health, and nginx keeps every backend in the pool |
| `dns` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `dns.resolver` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.ask` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.timeout` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.enabled` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.when_unavailable` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.only_with_header` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.send` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.accept` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.send_request` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `auth.jwt` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.issuer` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.audiences` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.keys` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.ca` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.header` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.optional` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.claims` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `auth.jwt.claims.<header name>` | supported | refused: nginx open source cannot verify a token (nginx Plus's auth\_jwt); verify it in the service, or render it for envoy |
| `remove_from_clients` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `cors` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `cors.allow_methods` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `cors.allow_headers` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `cors.expose_headers` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `cors.max_age` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `origins` | supported | supported |
| `origins.<environment name>` | supported | supported |
| `origins.<environment name>.hosts` | supported | partial: an empty Origin passes as none, where Envoy answers 403, and no browser sends one; a foreign origin to a path no route takes gets 403, where Envoy answers 404; refuses holding a host that starts or ends with .: nginx reads a dot at a host's start as every name under it and drops one at its end; write the host without it, or render it for envoy |
| `origins.<environment name>.origins` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `origins.<environment name>.patterns` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes` | supported | supported |
| `routes[].rpc` | supported | supported |
| `routes[].rpc.retries` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rpc.rate_limit` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rpc.rate_limit.requests` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rpc.rate_limit.per` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rpc.rate_limit.burst` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rpc.rate_limit.by` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rpc.to` | supported | supported |
| `routes[].rpc.to.<package pattern>` | supported | supported |
| `routes[].rpc.to.<package pattern>.service` | supported | supported |
| `routes[].rpc.to.<package pattern>.name` | supported | supported |
| `routes[].name` | supported | supported |
| `routes[].prefix` | supported | partial: any character but %, one Envoy escapes before it picks a route (" \< > ^ \` { \| }) and one outside ASCII; refuses holding %, a character Envoy escapes before it picks a route (" \< > ^ \` { \| }) or one outside ASCII: nginx matches a path holding a character Envoy escapes as it decodes it, where Envoy matches the escape, so a prefix or a path with % or such a character would take other requests on nginx than on Envoy; write it without them, as a pattern, or render it for envoy |
| `routes[].path` | supported | partial: any character but %, one Envoy escapes before it picks a route (" \< > ^ \` { \| }) and one outside ASCII; refuses holding %, a character Envoy escapes before it picks a route (" \< > ^ \` { \| }) or one outside ASCII: nginx matches a path holding a character Envoy escapes as it decodes it, where Envoy matches the escape, so a prefix or a path with % or such a character would take other requests on nginx than on Envoy; write it without them, as a pattern, or render it for envoy |
| `routes[].pattern` | supported | partial: matched on the path as the client wrote it, or on the one nginx has normalised when it holds //, a dot segment, an escaped unreserved character or %2F, as Envoy matches it; in a file with a pattern route, a path holding a character Envoy escapes before it matches (" \< > ^ \` { \| } or one outside ASCII) is answered 400, where Envoy matches the escape; a path PCRE2 gives up on past its match limit, as it does for (a\|aa)+ and a long run of a with another character after it, is answered 400, where Envoy's RE2 always decides whether the pattern takes it |
| `routes[].method` | supported | supported |
| `routes[].to` | supported | supported |
| `routes[].rewrite` | supported | partial: letters, digits and - . \_ ~ / ! $ & ' ( ) \* + , ; = : @ \[ \] alone; a request whose path keeps an escape Envoy does not decode, such as %3B or %20, or whose target ends in an empty query, a ? with nothing after it, is answered 400 on a route with a rewrite, where Envoy sends the rewritten path on; refuses holding ?: nginx reads a ? in a rewrite as the start of new arguments, after which it adds the client's; write the path without ?; refuses holding a character nginx escapes again: nginx escapes the new path before it sends it, so the service would receive that character escaped, where the Envoy output sends it as written; write the rewrite without it, or render it for envoy |
| `routes[].auth` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].retries` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rate_limit` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rate_limit.requests` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rate_limit.per` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rate_limit.burst` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `routes[].rate_limit.by` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `rpc` | supported | supported |
| `rpc.methods` | supported | supported |
| `rpc.methods[].service` | supported | supported |
| `rpc.methods[].method` | supported | supported |
| `rpc.methods[].action` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `rpc.methods[].reports` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `rpc.proto` | supported | supported |
| `rpc.proto.roots` | supported | supported |
| `rpc.proto.option` | supported | supported |
| `log` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `log.status_from` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `log.sample` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.provider` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.provider.xray` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.agent` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.segment` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.first` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rate` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rules` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rules[].host` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rules[].method` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rules[].path` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rules[].first` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.xray.sample.rules[].rate` | supported | refused: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter |
| `tracing.provider.otel` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.provider.otel.endpoint` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.provider.otel.service` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.provider.otel.sample` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.provider.otel.sample.rate` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.tags` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.tags.<tag name>` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.tags.<tag name>.env` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.tags.<tag name>.header` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.tags.<tag name>.default` | supported | refused: not written by this version's nginx generator; render it for envoy |
| `tracing.path_in_url` | supported | refused: not written by this version's nginx generator; render it for envoy |
| the request's path normalised before a route is picked | supported | partial: a path that holds //, a dot segment, an escaped unreserved character or %2F and also an escape Envoy keeps, such as %3B, is answered 400, and so is one that holds such an escape beside a character Envoy escapes before it matches (" \< > ^ \` { \| } or one outside ASCII), where Envoy normalises the path and keeps the escape; an empty query, a ? with nothing after it, after a path that needs normalising or holds such a character is answered 400, where Envoy sends the ? on; a backslash, %5C and a control character, written or escaped, are answered 400, and so is an invalid escape such as %zz, which Envoy passes on |
| a dot segment with a ; parameter answered 400 | supported | partial: /a/..;/b is answered 400 where Envoy 1.40 resolves it |
| the request line and header names read before a route is picked | supported | partial: a tab between the method and the target, an absolute target with user information, with no Host header or with a query or a fragment right after its host, and OPTIONS \* are answered 400, TRACE and CONNECT 405, and HTTP/0.9 gets no answer, where Envoy serves the first two and TRACE, answers OPTIONS \*, CONNECT and that absolute target 404, and HTTP/0.9 426; a header name outside RFC 9110's token characters is answered 400 over HTTP/1.1 and HTTP/2, where Envoy answers 400 over HTTP/1.1 and closes the connection over HTTP/2; a request trailer is never read, so over HTTP/1.1 one whose name is outside those characters is let through, not refused, where Envoy answers 400 (the service receives the body and no trailer through either), and over HTTP/2 any trailer ends the connection, where Envoy serves the request; a header line folded onto the next and a Host whose port is past 65535 or holds a sign are answered 400, where Envoy serves them |
<!-- /keys -->

## Written with no key

Each of these is part of every configuration the Envoy generator writes, with no key for it.
[Defaults](defaults.md) says what each does. Path normalisation, the refusal of a dot segment with a `;` parameter and
the reading of the request line and header names are core. All three are rows of the table above too, where every
generator declares how it writes them. The nginx and HAProxy columns name what each proxy offers, or that it is to be
settled with that proxy's generator:

| What | Envoy | nginx | HAProxy | Class |
|---|---|---|---|---|
| The request's path normalised before a route is picked | `normalize_path: true`, `merge_slashes: true` and `path_with_escaped_slashes_action: UNESCAPE_AND_FORWARD`: dot segments resolved, repeated slashes merged, `%2F` read as `/` | maps read the path as the client wrote it: with nothing to normalise, or only an escape Envoy keeps (such as `%3B`), routes are matched on it and the request is sent on as written; with `//`, a dot segment, an escaped unreserved character or `%2F` and no kept escape, on nginx's normalised URI, `$uri`, which is sent; a path that needs both, or holds a character Envoy escapes before it matches beside a kept escape or in a file with a pattern route, is answered `400`, and so is an empty query, a `?` with nothing after it, after a path matched on `$uri` | `http-request normalize-uri` with `path-merge-slashes`, `percent-decode-unreserved`, `path-strip-dot` and `path-strip-dotdot`, which its manual calls an experimental technical preview that needs `expose-experimental-directives`; `percent-decode-unreserved` decodes only letters, digits, `-`, `.`, `_` and `~`, so `%2F` stays as sent | core; HAProxy differs as its column says |
| A path that still holds a dot segment with a `;` parameter after normalisation answered `400`, the `;` written or escaped once (`%3B`, `%3b`) | the first route of every virtual host, `refused:dot-segment-with-parameter`: `safe_regex` `.*/\.\.?(?:;\|%3[Bb]).*` with `direct_response` `400` | a `map` on the decoded path, `$uri`, sets a flag for `/..;` and `/.;`, where an escaped `;` is decoded already, and the server answers the flag `400` before any route | to be settled with that generator | core |
| The request line and the header names read before a route is picked | no setting: Envoy's own codecs take an absolute target's host as the request's authority and answer a header name outside RFC 9110's token characters `400` over HTTP/1.1, closing the connection over HTTP/2 | a `map` on `$request` takes an absolute target's host, after one or more spaces, as the authority, and an empty one is answered `400`; the njs script `proxyfile.js`, written beside the configuration, reads each header name as the client sent it, and a name outside the token characters is answered `400` | to be settled with that generator | core |

The rest belong to Envoy:

| What | In the Envoy configuration | Class |
|---|---|---|
| The runtime switches | `gateway.ext_authz.enabled`, `gateway.request_log.min_status`, `gateway.request_log.sample`, `gateway.local_reply.origin_refused_status` and, with a `rate_limit`, `gateway.local_reply.rate_limited_status` | envoy |
| The retry conditions and the backend a retry goes to | `connect-failure,reset-before-request`, and the `previous_hosts` predicate with `host_selection_retry_max_attempts: 20` | envoy |
| The local replies | `502` with an empty body when a service cannot be reached, `403` with `Invalid CORS request` for a refused origin, with `auth.ask`, `403` for a check's 2xx other than `200`, and, with a `rate_limit`, the Connect error `resource_exhausted` for a Connect call over a limit | envoy |
| The Lua status fix | the Lua filter that sets Connect's current status on the bridge's errors, with `envoy.connect_bridge` | envoy |
| The auth check's mode | `ext_authz` in HTTP mode | envoy |

## The `nginx` and `haproxy` blocks

Each block holds the settings only its proxy reads beyond the core keys, and this version holds none. Written empty, it
is accepted; a key written in it is refused with `the nginx block takes no key in this version` (or `haproxy`), so a
file meant for a later version is never rendered as if the key were not there.

<!-- keys: nginx -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `nginx` | The settings only nginx reads beyond the core keys; this version holds none, so the block takes no key. | A mapping. | nginx |
<!-- /keys -->

<!-- keys: haproxy -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `haproxy` | The settings only HAProxy reads beyond the core keys; this version holds none, so the block takes no key. | A mapping. | haproxy |
<!-- /keys -->
