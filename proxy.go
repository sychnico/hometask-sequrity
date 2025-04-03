package main

import (
    "crypto/tls"
    "flag"
    "io"
    "log"
    "net"
    "net/http"
    "time"
    "strings"
    //"fmt"
)

var hopHeaders = []string{
    "Connection",
    "Keep-Alive",
    "Proxy-Authenticate",
    "Proxy-Authorization",
    "Te", // canonicalized version of "TE"
    "Trailers",
    "Transfer-Encoding",
    "Upgrade",
}

func copyHeader(dst, src http.Header) {
    for k, vv := range src {
        for _, v := range vv {
            dst.Add(k, v)
        }
    }
}

func delHopHeaders(header http.Header) {
    for _, h := range hopHeaders {
        header.Del(h)
    }
}

func appendHostToXForwardHeader(header http.Header, host string) {
    if prior, ok := header["X-Forwarded-For"]; ok {
        host = strings.Join(prior, ", ") + ", " + host
    }
    header.Set("X-Forwarded-For", host)
}


func serveHTTP(w http.ResponseWriter, req *http.Request) {
    
    log.Println(req.RemoteAddr, " ", req.Method, " ", req.URL)

    client := &http.Client{}

    req.RequestURI = ""

    delHopHeaders(req.Header)

    if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
        appendHostToXForwardHeader(req.Header, clientIP)
    }

    resp, err := client.Do(req)
    if err != nil {
        http.Error(w, "Server Error", http.StatusInternalServerError)
        log.Fatal("ServeHTTP:", err)
    }
    defer resp.Body.Close()

    log.Println(req.RemoteAddr, " ", resp.Status)

    delHopHeaders(resp.Header)

    copyHeader(w.Header(), resp.Header)
    w.WriteHeader(resp.StatusCode)
    io.Copy(w, resp.Body)
}

func serveConnect(w http.ResponseWriter, r *http.Request) {
    dest_conn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
    if err != nil {
        http.Error(w, err.Error(), http.StatusServiceUnavailable)
        return
    }

    w.WriteHeader(http.StatusOK)
    hijacker, ok := w.(http.Hijacker)
    if !ok {
        http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
        return
    }

    client_conn, _, err := hijacker.Hijack()
    if err != nil {
        http.Error(w, err.Error(), http.StatusServiceUnavailable)
    }

    go transfer(dest_conn, client_conn)
    go transfer(client_conn, dest_conn)
}

func transfer(destination io.WriteCloser, source io.ReadCloser) {
    defer destination.Close()
    defer source.Close()
    io.Copy(destination, source)
}

func main() {
    var pemPath string
    flag.StringVar(&pemPath, "pem", "server.pem", "path to pem file")
    var keyPath string
    flag.StringVar(&keyPath, "key", "server.key", "path to key file")
    var proto string
    flag.StringVar(&proto, "proto", "https", "Proxy protocol (http or https)")
    flag.Parse()

    if proto != "http" && proto != "https" {
        log.Fatal("Protocol must be either http or https")
    }

    server := &http.Server{
        Addr: ":8080",
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.Method == http.MethodConnect {
                serveConnect(w, r)
            } else {
                serveHTTP(w, r)
            }
        }),
        // Disable HTTP/2.
        TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
    }

    if proto == "http" {
        log.Fatal(server.ListenAndServe())
    } else {
        log.Fatal(server.ListenAndServeTLS(pemPath, keyPath))
    }
}