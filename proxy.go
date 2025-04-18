package main

import (
    "flag"
    "io"
    "log"
    "net"
    "net/http"
    "time"
)

var pemPath = "../server.pem"
var keyPath = "../server.key"

var hopHeaders = []string{
    "Connection",
    "Keep-Alive",
    "Proxy-Authenticate",
    "Proxy-Authorization",
    "Proxy-Connection",
    "Te", 
    "Trailers",
    "Transfer-Encoding",
    "Upgrade",
}

type ReqStruct struct {
    method string
    url string
    headers map[string][]string
    cookies []string
    body string
}

type RespStruct struct {
    status int
    headers map[string][]string
    cookies []string
    body string
}

var RequestDB = make([]ReqStruct, 8)
var ResponseDB = make([]RespStruct, 8)

func storeRequestInDB(req *http.Request) {
    reqCookies := req.Header["Cookie"]
    bytedata, err := io.ReadAll(req.Body)
    if err != nil {
        log.Fatal("Error with parsing request")
    }
    reqBodyString := string(bytedata)
    RequestDB = append(RequestDB, ReqStruct{method: req.Method, url: req.URL.String(), headers: req.Header, cookies: reqCookies, body: reqBodyString})
    //log.Println(RequestDB[len(RequestDB)-1])
}

func storeResponseInDB(resp *http.Response){
    respCookies := resp.Header["Cookie"]
    bytedata, err := io.ReadAll(resp.Body)
    if err != nil {
        log.Fatal("Error with parsing response")
    }
    respBodyString := string(bytedata)
    ResponseDB = append(ResponseDB, RespStruct{status: resp.StatusCode, headers: resp.Header, cookies: respCookies, body: respBodyString})
    //log.Println(ResponseDB[len(ResponseDB)-1])
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

func serveHTTP(w http.ResponseWriter, req *http.Request) {
    
    log.Println(req.RemoteAddr, " ", req.Method, " ", req.URL)

    client := &http.Client{}

    req.RequestURI = ""

    delHopHeaders(req.Header)

    resp, err := client.Do(req)
    if err != nil {
        http.Error(w, "Server Error", http.StatusInternalServerError)
        log.Fatal("ServeHTTP:", err)
    }
    defer resp.Body.Close()

    log.Println(req.RemoteAddr, " ", resp.Status)
    
    storeRequestInDB(req)
    storeResponseInDB(resp)

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
    var proto string
    flag.StringVar(&proto, "proto", "https", "Proxy protocol (http or https)")
    flag.Parse()

    if proto != "http" && proto != "https" {
        log.Fatal("Protocol must be either http or https")
    }

    server := &http.Server{
        Addr: ":8888",
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.Method == http.MethodConnect {
                serveConnect(w, r)
            } else {
                serveHTTP(w, r)
            }
        }),
    }

    if proto == "http" {
        log.Fatal(server.ListenAndServe())
    } else {
        log.Fatal(server.ListenAndServeTLS(pemPath, keyPath))
    }
}