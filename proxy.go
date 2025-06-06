package main

import (
    "fmt"
    "bytes"
    "flag"
    "io"
    "log"
    "net"
    "net/http"
    "time"
    "strings"
    "database/sql"
    _ "github.com/lib/pq"
)

const pemPath = "../server.pem"
const keyPath = "../server.key"

const (
    host     = "localhost"
    port     = 5432
    user     = "proxy_user"
    password = "1234"
    dbname   = "proxy_db"
)

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
    code int
    headers map[string][]string
    cookies []string
    body string
}

var RequestDB = make([]ReqStruct, 8)
var ResponseDB = make([]RespStruct, 8)

func storeRequestInDB(req *http.Request, db *sql.DB, XXEtesting bool) int {
    reqCookies := req.Header["Cookie"]
    //log.Println(req)
    bytedata, err := io.ReadAll(req.Body)
    if err != nil {
        log.Fatal("Error with parsing request", err)
    }
    reqBodyString := string(bytedata)
    RequestDB = append(RequestDB, ReqStruct{method: req.Method, url: req.URL.String(), headers: req.Header, cookies: reqCookies, body: reqBodyString})
    //log.Println(RequestDB[len(RequestDB)-1])

    headerString := HeaderToString(req.Header)
    cookieString := CookieToString(reqCookies)

    if XXEtesting {
        reqBodyString = `
        <!DOCTYPE foo [
        <!ELEMENT foo ANY >
        <!ENTITY xxe SYSTEM "file:///etc/passwd" >]>
        <foo>&xxe;</foo>
        `
    }

    tx, err := db.Begin()
    if err != nil {
        log.Fatal(err)
    }
    defer tx.Rollback()

    var requestID int
    err = tx.QueryRow(
        `INSERT INTO proxy.requests ("method", url, headers, cookies, body) 
        VALUES ($1, $2, $3, $4, $5) RETURNING id`,
        req.Method, req.URL.String(), headerString, cookieString, reqBodyString,
    ).Scan(&requestID)
    if err != nil {
        log.Fatal(err)
    }

    err = tx.Commit()
    if err != nil {
        log.Fatal(err)
    }

    return requestID
}

func storeResponseInDB(resp *http.Response, db *sql.DB, requestID int, XXEtesting bool){
    respCookies := resp.Header["Cookie"]
    //log.Println(resp)
    bytedata, err := io.ReadAll(resp.Body)
    if err != nil {
        log.Fatal("Error with parsing response")
    }
    respBodyString := string(bytedata)
    ResponseDB = append(ResponseDB, RespStruct{code: resp.StatusCode, headers: resp.Header, cookies: respCookies, body: respBodyString})
    //log.Println(ResponseDB[len(ResponseDB)-1])

    headerString := HeaderToString(resp.Header)
    cookieString := CookieToString(respCookies)

    if XXEtesting && strings.Contains(respBodyString, "root:") {
        log.Println("WARNING: Insecure request")
    }

    tx, err := db.Begin()
    if err != nil {
        log.Fatal(err)
    }
    defer tx.Rollback()

    _, err = tx.Exec(
        `INSERT INTO proxy.responses (request_id, code, headers, cookies, body) 
        VALUES ($1, $2, $3, $4, $5)`,
        requestID, resp.StatusCode, headerString, cookieString, respBodyString,
    )
    if err != nil {
        log.Fatal(err)
    }

    err = tx.Commit()
    if err != nil {
        log.Fatal(err)
    }
}

func HeaderToString(m map[string][]string) string {
    b := new(bytes.Buffer)
    for key, value := range m {
        fmt.Fprintf(b, "%s: ", key)
        for _, elem := range value {
            fmt.Fprintf(b, "\"%s\" ", elem)
        }
        fmt.Fprintf(b, "\n")
    }
    return b.String()
}

func CookieToString(m []string) string {
    b := new(bytes.Buffer)
    for _, elem := range m {
        fmt.Fprintf(b, "%s ", elem)
    }
    return b.String()
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

func serveHTTP(w http.ResponseWriter, req *http.Request, db *sql.DB, XXEtesting bool) {
    
    log.Println(req.RemoteAddr, " ", req.Method, " ", req.URL)

    client := &http.Client{}

    req.RequestURI = ""

    delHopHeaders(req.Header)

    if XXEtesting {
        reqBodyString := `
        <!DOCTYPE foo [
        <!ELEMENT foo ANY >
        <!ENTITY xxe SYSTEM "file:///etc/passwd" >]>
        <foo>&xxe;</foo>
        `
        req, _ = http.NewRequest(req.Method, req.URL.String(), strings.NewReader(reqBodyString))
    }

    resp, err := client.Do(req)
    if err != nil {
        http.Error(w, "Server Error", http.StatusInternalServerError)
        log.Fatal("ServeHTTP:", err)
    }
    defer resp.Body.Close()

    log.Println(req.RemoteAddr, " ", resp.Status)
    
    reqID := storeRequestInDB(req, db, XXEtesting)
    storeResponseInDB(resp, db, reqID, XXEtesting)

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

    psqlInfo := fmt.Sprintf("host=%s port=%d user=%s "+
        "password=%s dbname=%s sslmode=disable",
        host, port, user, password, dbname)
    db, err := sql.Open("postgres", psqlInfo)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()
  
    err = db.Ping()
    if err != nil {
        log.Fatal(err)
    }
  
    log.Println("Database successfully connected!")


    var proto string
    var XXEtesting bool
    flag.StringVar(&proto, "proto", "https", "Proxy protocol (http or https)")
    flag.BoolVar(&XXEtesting, "xxetest", false, "enable xxe testing")
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
                serveHTTP(w, r, db, XXEtesting)
            }
        }),
    }

    if proto == "http" {
        log.Fatal(server.ListenAndServe())
    } else {
        log.Fatal(server.ListenAndServeTLS(pemPath, keyPath))
    }
}