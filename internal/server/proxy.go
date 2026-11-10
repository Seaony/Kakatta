package server

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type requestPeer struct {
	IP     netip.Addr
	Host   string
	Secure bool
}

var errRequestPeer = errors.New("server.invalid_proxy")

func resolvePeer(r *http.Request, trusted []string) (requestPeer, error) {
	prefixes, err := proxyPrefixes(trusted)
	if err != nil {
		return requestPeer{}, errRequestPeer
	}
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return requestPeer{}, errRequestPeer
	}
	peer := requestPeer{IP: remote.Addr().WithZone("").Unmap(), Host: r.Host, Secure: r.TLS != nil}
	if !trustedProxy(peer.IP, prefixes) {
		return peer, nil
	}
	forwardedFor, exists, err := proxyHeader(r.Header, "X-Forwarded-For")
	if err != nil {
		return requestPeer{}, errRequestPeer
	}
	if exists {
		if len(forwardedFor) > 8192 {
			return requestPeer{}, errRequestPeer
		}
		parts := strings.Split(forwardedFor, ",")
		if len(parts) > 32 {
			return requestPeer{}, errRequestPeer
		}
		chain := make([]netip.Addr, len(parts))
		for i, part := range parts {
			ip, err := netip.ParseAddr(strings.Trim(part, " \t"))
			if err != nil || ip.Zone() != "" {
				return requestPeer{}, errRequestPeer
			}
			chain[i] = ip.Unmap()
		}
		// 左边可能是用户伪造的，只走到离服务器最近的不可信地址。
		for i := len(chain) - 1; i >= 0; i-- {
			peer.IP = chain[i]
			if !trustedProxy(peer.IP, prefixes) {
				break
			}
		}
	}
	proto, exists, err := proxyHeader(r.Header, "X-Forwarded-Proto")
	if err != nil {
		return requestPeer{}, errRequestPeer
	}
	if exists {
		switch strings.ToLower(strings.Trim(proto, " \t")) {
		case "http":
		case "https":
			peer.Secure = true
		default:
			return requestPeer{}, errRequestPeer
		}
	}
	host, exists, err := proxyHeader(r.Header, "X-Forwarded-Host")
	if err != nil {
		return requestPeer{}, errRequestPeer
	}
	if exists {
		if !proxyHost(host) {
			return requestPeer{}, errRequestPeer
		}
		peer.Host = host
	}
	return peer, nil
}

func proxyPrefixes(trusted []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(trusted))
	for _, entry := range trusted {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			ip, err := netip.ParseAddr(entry)
			if err != nil || ip.Zone() != "" {
				return nil, errRequestPeer
			}
			ip = ip.Unmap()
			prefix = netip.PrefixFrom(ip, ip.BitLen())
		} else if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func trustedProxy(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func proxyHeader(headers http.Header, name string) (string, bool, error) {
	values, exists := headers[http.CanonicalHeaderKey(name)]
	if !exists {
		return "", false, nil
	}
	if len(values) != 1 || values[0] == "" {
		return "", false, errRequestPeer
	}
	return values[0], true, nil
}

func proxyHost(host string) bool {
	if host == "" || !utf8.ValidString(host) {
		return false
	}
	for _, r := range host {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(",/\\?#@", r) {
			return false
		}
	}
	u, err := url.Parse("http://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil {
		return false
	}
	if strings.HasPrefix(host, "[") {
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return false
		}
	} else if strings.ContainsAny(u.Hostname(), ":[]") {
		return false
	}
	if port := u.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return false
		}
	} else if strings.HasSuffix(host, ":") {
		return false
	}
	return true
}
