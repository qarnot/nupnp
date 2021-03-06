package main

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	lifetime = 24 * time.Hour
	httpAddr = ":8180"
	dumpPath = ""
)

var devices struct {
	sync.RWMutex
	d []Device
}

type Device struct {
	ExternalAddress string    `json:"-"`
	InternalAddress string    `json:"internaladdress"`
	Port            int       `json:"port,omitempty"` // optional
	Name            string    `json:"name"`
	Added           time.Time `json:"added"`
}

func main() {
	flag.DurationVar(&lifetime, "lifetime", lifetime, "Maximal time an object will stay before")
	flag.StringVar(&httpAddr, "bind", httpAddr, "Bind to the given address:port")
	flag.StringVar(&dumpPath, "dump", dumpPath, "Location where store/load devices' dumps between restarts")
	flag.Parse()

	if _, err := os.Stat(dumpPath); dumpPath == "" || os.IsNotExist(err) {
		devices.d = make([]Device, 0)
	} else {
		log.Println("Resoring states from file: ", dumpPath)
		devices.d, err = loadDevices(dumpPath)
		if err != nil {
			log.Fatal("Unable to load saved states:", err)
		}
	}

	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {})
	http.HandleFunc("/api/register", RegisterDevice)
	http.HandleFunc("/api/devices", ListDevices)
	http.Handle("/", http.FileServer(http.Dir("public")))

	go cleanup()

	// Prepare graceful shutdown
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	srv := &http.Server{
		Addr: httpAddr,
	}

	// Serve content
	go func() {
		log.Fatal(srv.ListenAndServe())
	}()
	fmt.Println("listen on", httpAddr)

	// Wait shutdown signal
	<-interrupt

	log.Print("Saving registered hosts...")
	if err := saveDevices(dumpPath); err != nil {
		log.Fatal("error:", err)
	}
	log.Println("done")

	log.Print("The service is shutting down...")
	srv.Shutdown(context.Background())
	log.Println("done")
}

func saveDevices(dumpPath string) error {
	fd, err := os.Create(dumpPath)
	if err != nil {
		return err
	}
	defer fd.Close()

	devices.RLock()
	defer devices.RUnlock()

	return gob.NewEncoder(fd).Encode(devices.d)
}

func loadDevices(dumpPath string) (d []Device, err error) {
	var fd *os.File
	fd, err = os.Open(dumpPath)
	if err != nil {
		return
	}
	defer fd.Close()

	err = gob.NewDecoder(fd).Decode(&d)

	return
}

func findDevice(ia string, ea string) (int, bool) {
	for i, d := range devices.d {
		if d.InternalAddress == ia && d.ExternalAddress == ea {
			return i, true
		}
	}
	return -1, false
}

func devicesFor(ea string) []Device {
	found := []Device{}
	for _, d := range devices.d {
		if d.ExternalAddress == ea {
			found = append(found, d)
		}
	}
	return found
}

func RegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "Please send json", 400)
		return
	}

	if r.Body == nil {
		http.Error(w, "Please send a request body", 400)
		return
	}

	var t struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Port    int    `json:"port"`
	}

	err := json.NewDecoder(r.Body).Decode(&t)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	t.Address = strings.Trim(t.Address, " ")

	if net.ParseIP(t.Address) == nil {
		http.Error(w, t.Address+" is not a valid IP address", http.StatusBadRequest)
		return
	}

	// Prevent simple loopback mistake
	if t.Address == "127.0.0.1" || t.Address == "::1" {
		http.Error(w, `Loopback is not allowed`, http.StatusBadRequest)
		return
	}

	if net.ParseIP(t.Address) == nil {
		http.Error(w, `"address" is not a valid IP address`, http.StatusBadRequest)
		return
	}

	// TODO: validate parameter name required and no html/js
	ea, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Check if proxy was configured.
	if ea == "127.0.0.1" || ea == "::1" {
		xrealip := r.Header.Get("x-real-ip")
		if xrealip != "" {
			ea = xrealip
		} else {
			log.Println(ea, "tried to add an address, this can happen when proxy is not configured correctly.")
			http.Error(w, `Host `+ea+` is not allowed to register devices`, http.StatusBadRequest)
			http.NotFound(w, r)
			return
		}
	}

	devices.Lock()
	defer devices.Unlock()

	if i, ok := findDevice(t.Address, ea); ok {
		devices.d[i].Name = t.Name
		devices.d[i].Port = t.Port
		devices.d[i].Added = time.Now()
		log.Println("updated", t.Address)
	} else {
		devices.d = append(devices.d, Device{
			ExternalAddress: ea,
			InternalAddress: t.Address,
			Port:            t.Port,
			Name:            t.Name,
			Added:           time.Now(),
		})
		log.Println("added", t.Address)
	}

	scheme := r.Header.Get("x-forwarded-proto")
	if scheme == "" {
		scheme = "https"
	}
	host := r.Header.Get("host")
	if host == "" {
		host = "nupnp.com"
	}

	fmt.Fprintf(w, "Successfully added, visit %s://%s for more.\n", scheme, host)
}

func ListDevices(w http.ResponseWriter, r *http.Request) {
	ea, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Check if proxy was configured.
	if ea == "127.0.0.1" || ea == "::1" {
		xrealip := r.Header.Get("x-real-ip")
		if xrealip != "" {
			ea = xrealip
		} else {
			log.Println(ea, "tried to access an address, this can happen when proxy is not configured correctly.")
			http.NotFound(w, r)
			return
		}
	}

	devices.RLock()
	defer devices.RUnlock()

	ds := devicesFor(ea)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ds); err != nil {
		panic(err)
	}
}

func cleanup() {
	for {
		firstEvent := time.Now()
		devices.RLock()
		for _, d := range devices.d {
			if firstEvent.After(d.Added) {
				firstEvent = d.Added
			}
		}
		devices.RUnlock()

		time.Sleep(firstEvent.Add(lifetime).Add(time.Second).Sub(time.Now()))

		devices.Lock()
		for i := len(devices.d) - 1; i >= 0; i-- {
			d := devices.d[i]
			if time.Since(d.Added) > lifetime {
				log.Println("deleting", devices.d[i].InternalAddress, "(timeout)")
				devices.d = append(devices.d[:i], devices.d[i+1:]...)
			}
		}
		devices.Unlock()
	}
}
