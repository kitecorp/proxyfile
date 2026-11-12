# `tls`

`tls` makes the public listener serve HTTPS on the `listen` port, with a certificate and its key. Without it the
listener serves plain HTTP. With `clients`, the gateway also asks every client for a certificate of its own (mutual
TLS).

```yaml
listen: 8443
tls:
  certificate: /etc/gateway/tls/certificate.pem   # the certificate chain, PEM
  key: /etc/gateway/tls/key.pem                   # its private key, PEM
  clients: {ca: /etc/gateway/tls/clients-ca.pem}  # optional: the CA a client's certificate must chain to
```

- `certificate` and `key` are PEM files on the proxy's host. Each is an absolute path, so what a file names never
  depends on the directory the proxy starts in.
- With `clients`, a connection without a certificate that chains to `clients.ca` is refused during the handshake, before
  any request is read: one without a certificate, and one whose certificate another CA signed.
- The listener offers HTTP/2 and HTTP/1.1 over TLS (ALPN `h2` and `http/1.1`), so a client chooses either on the one
  port.
- The protocol versions and the cipher suites are Envoy's defaults, and the format has no key for them. Measured on
  Envoy 1.40.2: TLS 1.2 and 1.3 are accepted and TLS 1.0 and 1.1 refused, and a TLS 1.2 client gets ECDHE key exchange
  with AES-GCM or ChaCha20-Poly1305 and nothing else. Envoy says its defaults change over time, so they are measured
  again when proxyfile moves to another Envoy release.
- The admin listener stays plain HTTP on `127.0.0.1`.
- Renewing the certificate is the operator's work. The proxy reads the files when it starts, and Envoy reads them again
  on a hot restart; the key configures no secret discovery service (SDS), so a file replaced while the proxy runs is not
  read until then.
- A file with `tls` takes `client_address: connection` unless it writes `client_address`: the client's address is the
  peer of the connection. A gateway behind a proxy that sets `X-Forwarded-For` writes `client_address: forwarded`;
  [The listener](listener.md#the-clients-address) says what each value means.

[`examples/tls`](../examples/tls/) holds this gateway in [YAML](../examples/tls/yaml/proxyfile.yaml) and
[HOCON](../examples/tls/hocon/Proxyfile), each beside the `envoy.yaml` it gives.

<!-- keys: tls -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `tls` | The public listener serves HTTPS with this certificate and key; without it, plain HTTP. | A mapping. The proxy reads the files when it starts. | core |
| `tls.certificate` | The certificate chain, PEM, a file on the proxy's host. | Required. An absolute path. | core |
| `tls.key` | The private key, PEM, a file on the proxy's host. | Required. An absolute path. | core |
| `tls.clients` | Require a client certificate on every connection (mutual TLS). | A mapping. | core |
| `tls.clients.ca` | The CA bundle, PEM, that a client's certificate must chain to. | Required. An absolute path. | core |
<!-- /keys -->
