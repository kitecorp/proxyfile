# `auth`

`auth` is not an auth service. It checks each request before the proxy forwards it, in one of two ways or both:

- `ask` makes the proxy ask a service of yours "may this request pass?", and copy the identity headers your service
  answers onto the request. Your service decides, and the proxy acts on the answer. Envoy and nginx have this built
  in, as Envoy's `ext_authz` and nginx's `auth_request`, and HAProxy has it through an add-on
  ([Portability](portability.md)).
- `jwt` makes the proxy verify a signed token on the request itself, with no service of yours to ask
  ([Tokens](#tokens)). nginx open source has no token verification, and HAProxy's takes no key set, so their
  generators will refuse it ([Portability](portability.md)).

The rest of this page up to [Tokens](#tokens) is about `ask`, which the page calls the check.

```yaml
services:
  accounts: {http: 7070}   # your service that answers the question
auth:
  ask: accounts /check     # the service and the path the proxy asks about each request
  timeout: 2s              # how long the proxy waits for the answer
  enabled: true            # true runs the check; false turns it off for every request
  when_unavailable: deny   # deny refuses a request the check could not answer; allow lets it through
  accept: [x-user-id]      # the headers of the answer copied onto the request, such as who the user is
```

The check is a plain HTTP request to your service, so your service needs nothing made for one proxy. With the envoy
target it is Envoy's `ext_authz` in HTTP mode. How the check works:

- The check gets a request to the path of `ask`, never the client's path or query, because paths can carry links and
  tokens; only `send_request` tells it the client's path, in a header ([below](#what-the-check-is-told)). The headers
  it gets are the ones `send` names and the route's question below. HTTP mode adds the client's `Host`, the request's
  method, `Authorization` and `Content-Length: 0`, whatever `send` says, and with the envoy target Envoy's own client
  adds `x-envoy-internal: true` and `x-envoy-expected-rq-timeout-ms`, the deadline in milliseconds (measured on Envoy
  1.40.2).
- Each RPC route adds its question to the check: `x-gateway-action` holds the method's `action` and `x-gateway-reports`
  its `reports` joined by commas. A plain route adds neither. The question is for the check alone, and no service sees
  it.
- `x-gateway-timeout-ms` holds the deadline in milliseconds, the same as `auth.timeout`, so the auth service can keep
  its own calls inside it.
- The service answers `200` to let the request through. The headers of the answer named by `accept` are set on the
  request to the service, replacing any value there. A `5xx` counts as a failed call, which `when_unavailable` decides.
  Any other status is sent to the client as a denial, with the check's body and headers, a 2xx other than `200` as
  `403` ([below](#a-check-that-answers-another-2xx)). With the envoy target the denial carries no
  `x-envoy-upstream-service-time`, which Envoy's own client for the check adds ([Defaults](defaults.md)).
- With `when_unavailable: allow`, a request that could not be checked goes through with the header
  `x-envoy-auth-failure-mode-allowed` set, and the gateway writes a `gateway_auth_unavailable` log line for it. A
  client's own copy of that header is removed before anything reads it.
- A check sent on a connection the auth service is closing fails. Set `timeouts.upstream_idle` under the auth service's
  own idle timeout, so the gateway closes an idle connection first.

## What the check is told

With `send_request: true` the check also learns which request it is asked about, in the headers forward-auth services
read:

- `x-forwarded-uri`: the request's path and query;
- `x-forwarded-method`: its method;
- `x-forwarded-proto`: its scheme;
- `x-forwarded-host`: its host.

The request the check receives still goes to the path of `ask`. The proxy sets each of the four headers itself, so a
client's own `x-forwarded-uri`, `x-forwarded-method` and `x-forwarded-host` never reach the check, also when `send`
names them. Whose word the scheme is depends on [`client_address`](listener.md#the-clients-address). With
`connection` it is the connection's own. With `forwarded`, the default in a file without `tls`, it is the
`X-Forwarded-Proto` the request arrived with: a proxy in front sets it, and a client that reaches the gateway
directly writes it itself, so the check then reads `https` for a request sent over plain HTTP. A gateway that faces
its clients writes `client_address: connection`. The proxy sets `x-forwarded-uri`, `x-forwarded-method` and
`x-forwarded-host` on the check's request alone: a client's own copies reach the service as sent, unless
`remove_from_clients` names them.

The proxy normalises every request's path before anything reads it, with or without `send_request`
([A route with `auth: off`](#a-route-with-auth-off) says why): dot segments are resolved, repeated slashes merged and
escaped slashes unescaped, and an escaped letter is written out (`%61` as `a`). The check and the service then see the
same path. For the service this means that an escaped slash inside one segment arrives as a real slash: `/files/a%2Fb`
as `/files/a/b`. A backslash, written or as `%5C`, is read as a slash too. The proxy changes nothing else: a `;`
parameter on a name (`/admin;x=1`) and the case of a letter (`/ADMIN`) reach the check and the service as sent, so a
check that decides by path must read them as the service does.
`send_request` is off by default because a path can carry links and tokens. Measured on Envoy 1.40.2.

## A check that answers another 2xx

Only `200` lets a request through. A check that answers `201`, `202` or `204` refuses the request, and the client
receives `403`, with the check's body. 2.0 passed that 2xx on to the client, a success for a request that never reached
the service. Envoy's HTTP mode has no setting that lets another 2xx through. Measured on Envoy 1.40.2.

## A route with `auth: off`

A route with `auth: off` sends every request it takes to its service with no check and no token. The proxy normalises
the request's path before it picks the route: `/open/../private/report.txt` is read as `/private/report.txt` and does
not take a route with `prefix: /open/` and `auth: off`. Measured on Envoy 1.40.2 with `jwt` and Python's static file
server as the service: with no token, that path, `/open/%2e%2e/private/report.txt` and `/open/..%2fprivate/report.txt`
are each answered `401`, and no service is reached.

A dot segment with a `;` parameter is refused, the `;` written or escaped once. The proxy resolves `..;` written with
dots, but it writes `%2e%2e;` out as `..;` and leaves it there, and a servlet container drops the parameter and
resolves the dots: Tomcat 11 answered `/open/..;/private/report.txt` with `/private/report.txt`. The proxy keeps an
escaped `;`, `%3B` or `%3b`, as sent, so a hop that decodes it once on the way to the service would hand the service
that same `..;`. So the first route of every generated file answers `400` to a path that still holds `/.;`, `/..;`,
`/.%3B` or `/..%3B`, with the hex in either case, and such a request reaches no service. The route does not turn `auth`
off, so the token and the check are read before it answers: a request refused for its token or by the check gets that
answer, such as the check's `401`, instead of the `400`, also under a `prefix` with `auth: off`, and a request they let
through gets the `400`; neither reaches a service (measured on Envoy 1.40.2). A `;` escaped twice, `%253B`, passes as
sent: a service that decodes the path twice is not covered.

Envoy resolves `..;` written with dots itself from 1.40.0, and from 1.36.10, 1.37.6, 1.38.4 and 1.39.1 on the older
release lines, as their release notes say, while the runtime setting
`envoy.reloadable_features.strip_dotdot_segments_with_parameters` is on, its default. On an older Envoy, or with the
setting off, `..;` stays in the path and the same first route answers `400`: measured on Envoy 1.33.14, and on 1.40.2
with the setting off.

The proxy cannot read a path as every service does. A `;` parameter on a name, such as `/open/readme.txt;x=1`, and
the case of letters reach the route, the check and the service as sent. So write `prefix` with `auth: off` for a
service that holds nothing that needs the check, as the sign-in services of the recipes are. A route that must open
one path of a service that holds more is matched with `path`, not `prefix`.

## Recipes

Each recipe shows the services, the `auth` block and the routes it needs. The sign-in route comes before the
application's route, so it takes the sign-in pages first.

### Your own service

`ask` a route of your service that answers `200` to let a request through and `401` or `403` to refuse it. Set the
identity headers on the `200`, and name each one in `accept`. Add `send_request: true` when the check decides by the
request's path or method:

```yaml
services:
  accounts: {http: 7070}
  app: {http: 8080}
auth:
  ask: accounts /check
  timeout: 2s
  enabled: true
  when_unavailable: deny
  send: [cookie]
  accept: [x-user-id, x-user-roles]
routes:
  - {name: sign-in, prefix: /sign-in/, to: accounts, auth: off}
  - {name: app, prefix: /, to: app}
```

### oauth2-proxy

Measured on Envoy 1.40.2 with oauth2-proxy v7.15.6. The check asks oauth2-proxy at `/`, and its pages under `/oauth2/`
take a route with no check:

```yaml
services:
  oauth2-proxy: {http: 4180}
  app: {http: 8080}
auth:
  ask: oauth2-proxy /
  timeout: 2s
  enabled: true
  when_unavailable: deny
  send_request: true
  send: [cookie, accept]
  accept: [x-auth-request-user, x-auth-request-email, x-auth-request-groups, x-auth-request-preferred-username]
routes:
  - {name: sign-in, prefix: /oauth2/, to: oauth2-proxy, auth: off}
  - {name: app, prefix: /, to: app}
```

oauth2-proxy needs these settings, next to its own provider and cookie settings:

- `upstreams = ["static://200"]`: a signed-in request to any path outside `/oauth2/` is answered `200`, so the check
  lets it through.
- `set_xauthrequest = true`: the answer carries the `x-auth-request-*` identity headers that `accept` names.
- `reverse_proxy = true`: oauth2-proxy reads the `x-forwarded-*` headers `send_request` sets, so after sign-in it sends
  the user back to the page they asked for, not to `/`.
- `trusted_proxy_ips` set to the proxy's address: oauth2-proxy trusts those headers from every address when it is not
  set.
- `skip_provider_button = true`: a browser that is not signed in goes straight to the sign-in provider.

Do not `ask` oauth2-proxy's `/oauth2/auth`. It answers `202` to a signed-in request, which is a refusal here, so every
such request would get `403`.

### Authelia

Measured on Envoy 1.40.2 with Authelia v4.39.28. The check asks Authelia's `/api/verify`, with the https URL of
Authelia's portal, url-encoded, in `rd`:

```yaml
services:
  authelia: {http: 9091}
  app: {http: 8080}
auth:
  ask: authelia /authelia/api/verify?rd=https%3A%2F%2Fapp.example.com%2Fauthelia%2F
  timeout: 2s
  enabled: true
  when_unavailable: deny
  send_request: true
  send: [cookie, accept]
  accept: [remote-user, remote-groups, remote-email, remote-name]
routes:
  - {name: sign-in, prefix: /authelia/, to: authelia, auth: off}
  - {name: app, prefix: /, to: app}
```

Authelia needs these settings, next to the rest of its configuration:

- `server.address: tcp://:9091/authelia`, so its pages are under `/authelia/`, the sign-in route.
- A session cookie `domain` that covers the host: a name with a dot, because Authelia refuses `localhost`.
- An `authelia_url` with https, inside that cookie domain, such as `https://app.example.com/authelia`.

Authelia refuses every request whose `x-forwarded-proto` is not `https`, so the request must reach the proxy over https:
the proxy serves it with [`tls`](tls.md), or a proxy in front serves it and sends `X-Forwarded-Proto: https`, with
`client_address: forwarded`.

Do not `ask` Authelia's forward-auth endpoint, `/api/authz/forward-auth`. The check is sent with the client's method,
and that endpoint answers `405` to every method but `GET` and `HEAD`, and lets an `OPTIONS` request with no session
through with `200`.

### What both need

- `send: [cookie, accept]`. The `Cookie` header carries the session, and the `Accept` header lets the sign-in service
  answer a JSON client `401` and a browser a redirect to sign in.
- `send_request: true`. Without it oauth2-proxy sends the user to `/` after sign-in, and Authelia refuses every
  request.
- The sign-in pages on a route with `auth: off` ([what such a route takes](#a-route-with-auth-off)), or the sign-in
  callback is checked too and loops.
- No `only_with_header`: a request with no cookie would skip the check.
- The check is called with the client's method, so it must answer every method it should allow.
- On a refusal every header of the check's answer reaches the client, such as `Location` and `Set-Cookie`, which the
  sign-in needs.
- A header of the check's answer that `accept` does not name is not protected: a client's own copy reaches the
  service. Name every identity header the check sets in `accept`.

## Tokens

With `jwt` the proxy verifies a signed token, a JWT, on each request itself: its signature against your keys, its
issuer, its audience and its expiry. [`examples/jwt`](../examples/jwt/) verifies tokens with no check, in
[YAML](../examples/jwt/yaml/proxyfile.yaml) and [HOCON](../examples/jwt/hocon/Proxyfile):

```yaml
services:
  app: {http: 3000}
  identity: {http: 4000}                # the sign-in service, which issues the tokens
auth:
  jwt:
    issuer: https://login.example.com   # the iss a token must carry
    audiences: [api.example.com]        # its aud must hold one of them; any accepts every audience
    keys: /etc/gateway/jwt/jwks.json    # the signing keys: a JWKS file on the proxy's host, or an https URL
    claims:
      x-user-id: sub                    # the token's sub reaches the service in x-user-id
      x-user-roles: realm.roles         # a nested claim, written with dots
routes:
  - {name: sign-in, prefix: /login/, to: identity, auth: off}
  - {name: app, prefix: /, to: app}
```

What a request gets:

- A request without a valid token is answered `401` with `www-authenticate: Bearer`: no token, an expired token, one
  from another issuer or one whose signature fails. A token whose audience is not accepted is answered `403`. Either
  way the request reaches neither the check nor a service, unless it takes a route with `auth: off`
  ([what such a route takes](#a-route-with-auth-off)).
- A token with no expiry is refused, because it would be valid for ever. A token passes for up to 60 seconds after its
  expiry, which Envoy allows for clocks that differ.
- The token is read from `authorization`, after `Bearer ` written exactly so, or from the header `header` names, where
  it is written bare. A token anywhere else is never read: after `bearer` in lower case or `BEARER`, with no scheme, in
  a query parameter or in another header, the request counts as one with no token.
- The token stays on the request, so the service receives it too.
- With `optional: true` a request with no token goes through unverified, for a site with pages anyone may see. A request
  with a token that fails is still refused. A request that counts as one with no token goes through with whatever it
  carries: a forged token after `bearer` in lower case reached the service, still in `authorization`. So with
  `optional` the service takes the caller's identity from the `claims` headers only, which only a verified token
  sets, and never reads the token itself.
- `audiences: any` accepts every audience, so a token the same issuer minted for another application passes too. Write
  it on purpose.

The keys:

- `keys` is a JSON Web Key Set in a file on the proxy's host, or at an https URL the proxy fetches and keeps for 10
  minutes. A fetch that takes longer than 2 seconds fails. A fetch that cannot connect, or that is answered with a 5xx
  status, is tried once more. The second try goes to the host's next address, so a keys host that listens at only one
  of its addresses still serves the keys. While the URL cannot be fetched every token is refused with `401`, and the
  next request tries the fetch again.
- The URL's certificate must chain to the CA bundle `ca` names, `/etc/ssl/certs/ca-certificates.crt` when not written,
  and must name the URL's host. A `ca` path that does not exist stops the proxy at start. A name in the URL is looked
  up as a service's address is, through [`dns.resolver`](services.md#name-lookups) when it is written.

The claims:

- `claims` copies claims of a verified token into request headers for the service. A claim that is not text, such as
  a list of roles, arrives as the base64 of its JSON.
- A client's own copy of a `claims` header is removed before anything reads it, so only the token can set it.
- A `claims` header is not one that `send` or `accept` names: the check would receive the claim in place of the
  client's header, and its answer would replace the verified claim.

With `ask` and `jwt` together:

- The token is verified first, and the check runs only on the requests that passed. The token reaches the check: in
  `authorization`, which the check always receives, or in another header when `send` names it.
- `enabled: false` and `only_with_header` govern the check only; the token is still verified.
- `auth: off` on a route skips both ([what such a route takes](#a-route-with-auth-off)).

Before either: a request over a [rate limit](routes.md#rate-limits) is refused before its token is verified, and a
browser's CORS preflight is answered before either runs, as it carries no token.

Each behaviour in this section was measured on Envoy 1.40.2, except the 10 minutes, which are Envoy's default, and the
2 seconds, which the generated file sets.

The example's `/etc/gateway/jwt/jwks.json` is a path on the proxy's host, or in its container.
`scripts/validate-envoy.sh` validates each example with a throwaway key set and CA bundle mounted at
`/etc/gateway/jwt`, made for each run and removed after it.

## Keys

<!-- keys: auth -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `auth` | The checks every request goes through before it reaches a service: a service of yours asked about each request, a signed token the proxy verifies, or both. | A mapping. At least one of `ask` and `jwt`. Without `auth` there is no check. | core |
| `auth.ask` | The service and the path the gateway asks. | A service and a path, such as accounts /check. The service needs an `http` port. | core |
| `auth.timeout` | How long the gateway waits for the answer. | A time such as 2s. Required with `ask`, and refused without it. At most `4294967295s`. `none` is not accepted. | core |
| `auth.enabled` | true runs the check on every request it applies to, false on none. | `true` or `false`. Required with `ask`, and refused without it. It does not turn `jwt` off. | core |
| `auth.when_unavailable` | What happens to a request when the check cannot be asked: allow lets it through, deny refuses it. | `allow` or `deny`. Required with `ask`, and refused without it. | core |
| `auth.only_with_header` | The check runs only on requests that carry this header. | A lower-case header name. A request without it, or with it empty, goes through with no check. Needs `ask`. | core |
| `auth.send` | The request headers the check is sent; a name ending in \* is a prefix. | A list; each item is a lower-case header name, or a prefix ending in \*. Needs `ask`. | core |
| `auth.accept` | The headers of the check's answer that reach the service; a name ending in \* is a prefix. | A list; each item is a lower-case header name, or a prefix ending in \*. Needs `ask`. | core |
| `auth.send_request` | true also tells the check which request it is asked about: its path and query in x-forwarded-uri, its method in x-forwarded-method, its scheme in x-forwarded-proto and its host in x-forwarded-host; false when not written. | `true` or `false`. Needs `ask`. | core |
| `auth.jwt` | The proxy verifies a signed token (a JWT) on each request itself: its signature against your keys, its issuer, its audience and its expiry. | A mapping. A request without a valid token is answered 401, or 403 when its audience is not accepted, and reaches neither the check nor a service, unless its route writes `auth: off`. A token with no expiry is refused. | core |
| `auth.jwt.issuer` | The iss claim a token must carry. | Required. One character or more. | core |
| `auth.jwt.audiences` | The audiences accepted: a token's aud must hold one of them, or any accepts every audience. | Required. A list of audiences, or any. `any` lets a token minted for another application of the same issuer through, so it is written on purpose. | core |
| `auth.jwt.keys` | Where the signing keys come from, as a JWKS: a file on the proxy's host, or an https URL the proxy fetches and keeps for 10 minutes. | Required. An absolute path, or a URL starting with https://. | core |
| `auth.jwt.ca` | The CA bundle, PEM, the keys URL's certificate must chain to; /etc/ssl/certs/ca-certificates.crt when not written. | An absolute path. Only with a URL in `keys`. A path that does not exist stops the proxy at start. | core |
| `auth.jwt.header` | The header that carries the token; authorization, with the Bearer scheme, when not written. | A lower-case header name. Any other header carries the bare token. A token anywhere else, such as in the query, is never read. Not `cookie`. | core |
| `auth.jwt.optional` | true lets a request with no token through unverified; a request with a token that fails is still refused. false when not written. | `true` or `false`. A token the proxy does not read, such as one after `bearer` in lower case, counts as no token, so with `true` the request passes and the service receives that token unverified. | core |
| `auth.jwt.claims` | Claims copied into request headers for the service, by header name. | A mapping keyed by header name. Not a header `send` or `accept` names, nor the header that carries the token. A claim that is not text arrives as the base64 of its JSON. | core |
| `auth.jwt.claims.<header name>` | The claim it holds, with dots for a nested claim. | A claim name, with dots for a nested claim, such as realm.roles. The header name is a lower-case header name. | core |
<!-- /keys -->
