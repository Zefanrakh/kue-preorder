package httpserver

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientIPKey struct{}

// ClientIP puts the caller's address in the request context, for rate limits
// (§22). header names where the proxy in front puts it (config.ClientIPHeader);
// for X-Forwarded-For the last entry, the one the proxy appended, is used.
// When header is empty, or a request lacks it because it did not come through
// the proxy, the connection's address is used.
//
// IPv6 addresses are cut to their /64 network: one subscriber usually holds
// a whole /64 and could otherwise use a fresh address for every request.
func ClientIP(header string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, clientIP(r, header))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClientIPFrom returns the address ClientIP stored, or "" outside it.
func ClientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

func clientIP(r *http.Request, header string) string {
	if header != "" {
		if values := r.Header.Values(header); len(values) > 0 {
			v := values[len(values)-1]
			if strings.EqualFold(header, "X-Forwarded-For") {
				parts := strings.Split(v, ",")
				v = parts[len(parts)-1]
			}
			if ip, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
				return network(ip)
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return network(ip)
	}
	return host
}

func network(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}
