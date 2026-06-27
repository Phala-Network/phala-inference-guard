package httpx

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func CloneHeader(header http.Header) http.Header {
	cloned := make(http.Header, len(header))
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func RemoveHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.TrimSpace(token); token != "" {
				header.Del(token)
			}
		}
	}
	for _, key := range hopByHopHeaders {
		header.Del(key)
	}
}

func AddXForwardedFor(out *http.Request, in *http.Request) {
	clientIP, _, err := net.SplitHostPort(in.RemoteAddr)
	if err != nil {
		return
	}
	prior := out.Header.Get("X-Forwarded-For")
	if prior != "" {
		clientIP = prior + ", " + clientIP
	}
	out.Header.Set("X-Forwarded-For", clientIP)
}

func JoinURLPath(target, request *url.URL) string {
	if target == nil {
		return request.Path
	}
	targetPath := target.EscapedPath()
	requestPath := request.EscapedPath()
	switch {
	case targetPath == "" || targetPath == "/":
		return requestPath
	case requestPath == "":
		return targetPath
	case strings.HasSuffix(targetPath, "/") && strings.HasPrefix(requestPath, "/"):
		return targetPath + requestPath[1:]
	case strings.HasSuffix(targetPath, "/") || strings.HasPrefix(requestPath, "/"):
		return targetPath + requestPath
	default:
		return targetPath + "/" + requestPath
	}
}

func CopyHeader(dst, src http.Header) {
	RemoveHopByHopHeaders(src)
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
