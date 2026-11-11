# `services`

`services` names the services behind the gateway, on the same host, and gives each its ports. [Defaults](defaults.md)
says which clusters a service gets.

<!-- keys: services -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `services` | The services behind the gateway, on the same host, by name. | Required. A mapping keyed by service name. At least one service. | core |
| `services.<service name>` | One service: a port per protocol, an optional Host header and an optional address. | A mapping. The service name is lower-case letters, digits and -, starting with a letter. It has at least one port. | core |
| `services.<service name>.http` | The service's HTTP port. | A whole number from 1 to 65535. Spoken as HTTP/1.1. | core |
| `services.<service name>.grpc` | The service's gRPC port. | A whole number from 1 to 65535. Spoken as HTTP/2 without TLS (h2c). | core |
| `services.<service name>.host_header` | The Host header a request carries when a route sends it to this service. | A host, with an optional port. Without it a request keeps the client's Host. | core |
| `services.<service name>.address` | The service's IP address; 127.0.0.1 when not written. | An IP address. Not a name: Envoy looks up no name for a static cluster. | core |
<!-- /keys -->
