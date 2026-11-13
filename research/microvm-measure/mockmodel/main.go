// Command mockmodel serves the real-agent suite's mock model API
// (internal/server/mockmodel_integration_test.go, copied in by build.sh)
// on the KVM host, and logs every request with its arrival time and the
// script it plays.
package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var lastScript = regexp.MustCompile(`aether-script:([^;"\\]+);`)

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "comma-separated listen addresses")
	m := &mockModel{answer: "aether-turn-done"}
	flag.Func("script", "name=command: a one-step script that runs command, repeatable", func(v string) error {
		name, command, _ := strings.Cut(v, "=")
		m.script(name, step{command: command})
		return nil
	})
	flag.Parse()
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		script := "-"
		if all := lastScript.FindAllSubmatch(body, -1); len(all) > 0 {
			script = string(all[len(all)-1][1])
		}
		m.ServeHTTP(w, r)
		log.Printf("%s %s %s %s script=%s bytes=%d took=%s", start.UTC().Format(time.RFC3339Nano), r.RemoteAddr, r.Method, r.URL.Path, script, len(body), time.Since(start))
	})
	errs := make(chan error)
	for _, addr := range strings.Split(*listen, ",") {
		go func() { errs <- http.ListenAndServe(addr, handler) }()
	}
	log.Fatal(<-errs)
}
