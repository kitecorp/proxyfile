package nginx

import (
	"net"
	"strconv"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// A collector by DNS name is reached through a relay in the http block. ngx_otel_module hands its endpoint to gRPC as
// written, and gRPC looked the name up once in each worker as it started and never again, so after the name moved to a
// second collector no span reached it until nginx reloaded, where the Envoy output looks its name up every 5 s. The
// relay is an upstream that looks the name up through dns.resolver, as a service's upstream does (upstreamLines), and a
// server on a socket file that passes each of the tracer's calls on to it. Through the relay the spans reached the
// second collector after the first one left and its name moved, and after the name moved with the first one still up,
// with no span lost in that case. The tracer's own lookup followed neither move, with the name written bare, as
// dns:///<name> or as dns://<name server>/<name>, and nor did a relay in the stream block, which picks the collector
// once for the tracer's one connection (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2; docs/nginx.md gives
// the counts). A collector by IP address is asked directly.

// relayed says whether the spans go through the relay: tracing to a collector by DNS name, which the support table
// admits only beside dns.resolver, as it admits a service's.
func relayed(t *model.Tracing) bool {
	return t != nil && t.OTel != nil && t.OTel.DNS
}

// collectorSocket is the socket file the relay listens on and the tracer sends to, in relayFolder beside the check
// relay's, so only nginx's own users can reach it.
func collectorSocket(listen int64) string {
	return relayFolder(listen) + "/collector.sock"
}

// collectorUpstream names the relay's upstream and its zone. No other can take the name: a service's name holds no _,
// so its upstreams are <service>_<protocol> and <service>_<protocol>_b<n>, and the check relay's is
// proxyfile_<listen>_check.
func collectorUpstream(listen int64) string {
	return "proxyfile_" + strconv.FormatInt(listen, 10) + "_collector"
}

// collectorEndpoint is the tracer's endpoint: the relay's socket for a collector by name, and otherwise its IP address,
// an IPv6 one in brackets.
func collectorEndpoint(otel *model.OTel, listen int64) string {
	if otel.DNS {
		return "unix:" + collectorSocket(listen)
	}
	return net.JoinHostPort(otel.Host, strconv.FormatInt(otel.Port, 10))
}

// relayTime is how long the relay waits on the tracer or on the collector for a read or a write. The module gives its
// calls no deadline and keeps four batches of spans in each worker, and a batch comes back only when its call ends, so a
// collector that takes calls and never answers held every batch, and nginx dropped each later span, where through the
// relay, which ends such a call, the module went on sending (measured on nginx 1.30.6 and 1.31.8). 10 s is twice the
// 5 s the slowest collector measured took to answer each call. The Envoy output's tracer gives its calls no deadline
// either (grpc_trace_exporter.cc in Envoy 1.40.2), but does not wait for one call to end before it sends the next.
const relayTime = 10 * 1000

// collectorLines are the relay's upstream and server, nil unless the spans go through the relay, with the relay's
// folder when the check, which writes it otherwise, is not asked. The server takes nothing from what the file says of
// its clients' requests, as the Envoy output's tracer is under none of its listener's settings: no span of its own
// (otel_trace off), no log line, no rate limit or check, which only a route's location writes, the header limits of a
// file with no limits key (headerDirectives), which hold the tracer's few headers whatever limits says, and the relay's
// own times in place of the file's timeouts. A batch's body has no size limit: its only client is the tracer, on a
// socket only nginx's users reach, and a batch of 512 spans of the largest file render writes must pass as it does to a
// collector asked directly; the gRPC module passes a body on as it reads it and keeps none (measured with the 453-tag
// file: every span arrived, and nginx wrote no body to a file). The upstream keeps no idle connection, so each call
// opens its own, and a call after the name moved goes to the new address.
func collectorLines(v *values, g *model.Gateway, e edge) []string {
	t := otelTracing(g)
	if !relayed(t) {
		return nil
	}
	listen := g.Listen.Number
	name := collectorUpstream(listen)
	var lines []string
	if asked(g) == nil {
		lines = folderLines(g)
	}
	collector := model.Service{Addresses: []model.Text{{Value: t.OTel.Host}}, DNS: true}
	lines = append(lines, upstreamLines(v, name, collector, model.ServicePort{Number: t.OTel.Port}, e)...)
	lines = append(lines, "  }", "  server {",
		"    listen unix:"+collectorSocket(listen)+";",
		// The tracer speaks gRPC, HTTP/2 from its first byte.
		"    http2 on;",
		"    otel_trace off;",
		"    access_log off;",
		"    client_max_body_size 0;",
	)
	limits, _ := headerDirectives(v, model.Limits{})
	for _, directive := range limits {
		lines = append(lines, "    "+directive)
	}
	return append(lines,
		"    client_body_timeout "+nginxTime(relayTime)+";",
		"    send_timeout "+nginxTime(relayTime)+";",
		"    location / {",
		"      grpc_pass grpc://"+name+";",
		// The Envoy output writes no connect time on its collector's cluster, so Envoy's own of 5 s applies, as on a
		// service's cluster.
		"      grpc_connect_timeout "+nginxTime(connectTimeout)+";",
		"      grpc_read_timeout "+nginxTime(relayTime)+";",
		"      grpc_send_timeout "+nginxTime(relayTime)+";",
		"    }",
		"  }",
	)
}
