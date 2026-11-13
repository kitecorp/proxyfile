# Portability

proxyfile writes Envoy's configuration today, and the format is written so that the same file can drive nginx and
HAProxy once each has a generator. Every key is **core** or belongs to one proxy. A core key is one every proxy can
carry, at times with a difference this page names, or one a proxy refuses by name where it cannot carry it. A key that
belongs to one proxy lives in that proxy's block at the top of the file: `envoy`, `nginx` or `haproxy`. A render for
one target reads the core keys and its own block and leaves the other blocks alone, so one file can hold what each
proxy needs. The split follows the Kubernetes Gateway API, which sorts its features into core, extended and
implementation-specific ones and is implemented by Envoy, nginx and HAProxy alike.

`proxyfile render --target envoy` is the default, and Envoy is the only target in this version. `--target nginx` and
`--target haproxy` are refused, and so is any key written in the `nginx` or `haproxy` block, until that proxy has a
generator. A target that cannot carry a key the file uses will refuse the file, naming the key and what that proxy
offers instead; it never writes a quietly weaker configuration.

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
| `timeouts.request` | the route timeout, for the whole request | none (`proxy_read_timeout` is per read) | `timeout http-request` for the headers, `timeout server` after them | core; nginx refuses it, HAProxy's differs as its column says |
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
| `routes[].retries`, `rpc.retries` | a retry policy with Envoy's conditions | `proxy_next_upstream` and `proxy_next_upstream_tries` | `retries` and `retry-on` | core; what "never reached the service" means differs by proxy |
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
`refused` with what that proxy offers instead. `proxyfile render` and `proxyfile check` with `--target <proxy>` refuse
a file that writes a key its target refuses, naming the key where it is written and what the proxy offers instead.
This table is written from those declarations and has a column for each proxy with a generator, so it holds Envoy's
alone in this version; the table above stays until the nginx and HAProxy columns are written here. The
[conformance battery](conformance.md) sends the same requests to each proxy, and checks the refusal for a key the proxy
refuses.

<!-- keys: support by proxy -->
| Key | Envoy |
|---|---|
| `listen` | supported |
| `admin` | supported |
| `tls` | supported |
| `tls.certificate` | supported |
| `tls.key` | supported |
| `tls.clients` | supported |
| `tls.clients.ca` | supported |
| `client_address` | supported |
| `limits` | supported |
| `limits.connections` | supported |
| `limits.request_headers` | supported |
| `limits.request_headers.size` | supported |
| `limits.request_headers.count` | supported |
| `timeouts` | supported |
| `timeouts.request` | supported |
| `timeouts.idle` | supported |
| `timeouts.upstream_idle` | supported |
| `services` | supported |
| `services.<service name>` | supported |
| `services.<service name>.http` | supported |
| `services.<service name>.grpc` | supported |
| `services.<service name>.host_header` | supported |
| `services.<service name>.address` | supported |
| `services.<service name>.addresses` | supported |
| `services.<service name>.balance` | supported |
| `services.<service name>.hash` | supported |
| `services.<service name>.hash.header` | supported |
| `services.<service name>.hash.client` | supported |
| `services.<service name>.health` | supported |
| `services.<service name>.health.path` | supported |
| `services.<service name>.health.grpc` | supported |
| `services.<service name>.health.interval` | supported |
| `services.<service name>.health.timeout` | supported |
| `services.<service name>.health.healthy` | supported |
| `services.<service name>.health.unhealthy` | supported |
| `dns` | supported |
| `dns.resolver` | supported |
| `auth` | supported |
| `auth.ask` | supported |
| `auth.timeout` | supported |
| `auth.enabled` | supported |
| `auth.when_unavailable` | supported |
| `auth.only_with_header` | supported |
| `auth.send` | supported |
| `auth.accept` | supported |
| `auth.send_request` | supported |
| `auth.jwt` | supported |
| `auth.jwt.issuer` | supported |
| `auth.jwt.audiences` | supported |
| `auth.jwt.keys` | supported |
| `auth.jwt.ca` | supported |
| `auth.jwt.header` | supported |
| `auth.jwt.optional` | supported |
| `auth.jwt.claims` | supported |
| `auth.jwt.claims.<header name>` | supported |
| `remove_from_clients` | supported |
| `cors` | supported |
| `cors.allow_methods` | supported |
| `cors.allow_headers` | supported |
| `cors.expose_headers` | supported |
| `cors.max_age` | supported |
| `origins` | supported |
| `origins.<environment name>` | supported |
| `origins.<environment name>.hosts` | supported |
| `origins.<environment name>.origins` | supported |
| `origins.<environment name>.patterns` | supported |
| `routes` | supported |
| `routes[].rpc` | supported |
| `routes[].rpc.retries` | supported |
| `routes[].rpc.rate_limit` | supported |
| `routes[].rpc.rate_limit.requests` | supported |
| `routes[].rpc.rate_limit.per` | supported |
| `routes[].rpc.rate_limit.burst` | supported |
| `routes[].rpc.rate_limit.by` | supported |
| `routes[].rpc.to` | supported |
| `routes[].rpc.to.<package pattern>` | supported |
| `routes[].rpc.to.<package pattern>.service` | supported |
| `routes[].rpc.to.<package pattern>.name` | supported |
| `routes[].name` | supported |
| `routes[].prefix` | supported |
| `routes[].path` | supported |
| `routes[].pattern` | supported |
| `routes[].method` | supported |
| `routes[].to` | supported |
| `routes[].rewrite` | supported |
| `routes[].auth` | supported |
| `routes[].retries` | supported |
| `routes[].rate_limit` | supported |
| `routes[].rate_limit.requests` | supported |
| `routes[].rate_limit.per` | supported |
| `routes[].rate_limit.burst` | supported |
| `routes[].rate_limit.by` | supported |
| `rpc` | supported |
| `rpc.methods` | supported |
| `rpc.methods[].service` | supported |
| `rpc.methods[].method` | supported |
| `rpc.methods[].action` | supported |
| `rpc.methods[].reports` | supported |
| `rpc.proto` | supported |
| `rpc.proto.roots` | supported |
| `rpc.proto.option` | supported |
| `log` | supported |
| `log.status_from` | supported |
| `log.sample` | supported |
| `tracing` | supported |
| `tracing.provider` | supported |
| `tracing.provider.xray` | supported |
| `tracing.provider.xray.agent` | supported |
| `tracing.provider.xray.segment` | supported |
| `tracing.provider.xray.sample` | supported |
| `tracing.provider.xray.sample.first` | supported |
| `tracing.provider.xray.sample.rate` | supported |
| `tracing.provider.xray.sample.rules` | supported |
| `tracing.provider.xray.sample.rules[].host` | supported |
| `tracing.provider.xray.sample.rules[].method` | supported |
| `tracing.provider.xray.sample.rules[].path` | supported |
| `tracing.provider.xray.sample.rules[].first` | supported |
| `tracing.provider.xray.sample.rules[].rate` | supported |
| `tracing.provider.otel` | supported |
| `tracing.provider.otel.endpoint` | supported |
| `tracing.provider.otel.service` | supported |
| `tracing.provider.otel.sample` | supported |
| `tracing.provider.otel.sample.rate` | supported |
| `tracing.tags` | supported |
| `tracing.tags.<tag name>` | supported |
| `tracing.tags.<tag name>.env` | supported |
| `tracing.tags.<tag name>.header` | supported |
| `tracing.tags.<tag name>.default` | supported |
| `tracing.path_in_url` | supported |
<!-- /keys -->

## Written with no key

Each of these is part of every configuration the Envoy generator writes, with no key for it.
[Defaults](defaults.md) says what each does. Path normalisation and the refusal of a dot segment with a `;` parameter
are core. The nginx and HAProxy columns name what each proxy offers, or that it is to be settled with that proxy's
generator:

| What | Envoy | nginx | HAProxy | Class |
|---|---|---|---|---|
| The request's path normalised before a route is picked | `normalize_path: true`, `merge_slashes: true` and `path_with_escaped_slashes_action: UNESCAPE_AND_FORWARD`: dot segments resolved, repeated slashes merged, `%2F` read as `/` | `location` matches a normalised URI: `%XX` decoded, `.` and `..` resolved, adjacent slashes merged (`merge_slashes`, on by default) | `http-request normalize-uri` with `path-merge-slashes`, `percent-decode-unreserved`, `path-strip-dot` and `path-strip-dotdot`, which its manual calls an experimental technical preview that needs `expose-experimental-directives`; `percent-decode-unreserved` decodes only letters, digits, `-`, `.`, `_` and `~`, so `%2F` stays as sent | core; HAProxy differs as its column says |
| A path that still holds a dot segment with a `;` parameter after normalisation answered `400` | the first route of every virtual host, `refused:dot-segment-with-parameter`: `safe_regex` `.*/\.\.?;.*` with `direct_response` `400` | to be settled with that generator | to be settled with that generator | core |

The rest belong to Envoy:

| What | In the Envoy configuration | Class |
|---|---|---|
| The runtime switches | `gateway.ext_authz.enabled`, `gateway.request_log.min_status`, `gateway.request_log.sample`, `gateway.local_reply.origin_refused_status` and, with a `rate_limit`, `gateway.local_reply.rate_limited_status` | envoy |
| The retry conditions | `connect-failure,reset-before-request` | envoy |
| The local replies | `502` with an empty body when a service cannot be reached, `403` with `Invalid CORS request` for a refused origin, with `auth.ask`, `403` for a check's 2xx other than `200`, and, with a `rate_limit`, the Connect error `resource_exhausted` for a Connect call over a limit | envoy |
| The Lua status fix | the Lua filter that sets Connect's current status on the bridge's errors, with `envoy.connect_bridge` | envoy |
| The auth check's mode | `ext_authz` in HTTP mode | envoy |

## The `nginx` and `haproxy` blocks

Each block is reserved for its proxy's generator. Written empty, it is accepted; a key written in it is refused with
`no generator for nginx in this version` (or `haproxy`), so a file meant for a later version is never rendered as if
the key were not there.

<!-- keys: nginx -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `nginx` | The settings only nginx will read; this version has no nginx generator, so it takes no key. | A mapping. | nginx |
<!-- /keys -->

<!-- keys: haproxy -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `haproxy` | The settings only HAProxy will read; this version has no HAProxy generator, so it takes no key. | A mapping. | haproxy |
<!-- /keys -->
