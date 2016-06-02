package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const lifetime time.Duration = 24 * time.Hour
const httpAddr = ":8080"
const fallback = "http://example.org"

var devices struct {
	sync.Mutex
	d map[string]*device
}

type device struct {
	id      string
	name    string
	address string
	added   time.Time
}

func main() {
	devices.d = make(map[string]*device)

	http.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		name := r.URL.Query().Get("name")
		newAddress := "http://" + r.URL.Query().Get("address")

		// TODO: check parameter newAddress
		// TODO: validate parameter newAddress
		ra := r.RemoteAddr

		devices.Lock()
		defer devices.Unlock()
		devices.d[ra] = &device{
			id:      id,
			name:    name,
			address: newAddress,
			added:   time.Now(),
		}

		cleanup()
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ra := r.RemoteAddr

		devices.Lock()
		defer devices.Unlock()
		if d, ok := devices.d[ra]; ok {
			http.Redirect(w, r, d.address, 302)
		} else {
			http.Redirect(w, r, fallback, 302)
		}
	})

	fmt.Println("listen on", httpAddr)
	// TODO: use http.ListenAndServeTLS(":443", "cert.pem", "key.pem", nil)
	log.Fatal(http.ListenAndServe(httpAddr, nil))

}

func cleanup() {
	for key, d := range devices.d {
		if time.Since(d.added) > lifetime {
			delete(devices.d, key)
		}
	}
}
