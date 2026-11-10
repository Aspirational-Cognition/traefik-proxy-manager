// Command tpm serves the Proxy Hosts UI and Traefik's HTTP-provider config.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Aspirational-Cognition/traefik-proxy-manager/internal/app"
)

func main() {
	a, err := app.New(os.Getenv, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tpm can't start:\n%v\n", err)
		os.Exit(1)
	}
	addr := fmt.Sprintf(":%d", a.Port)
	log.Printf("tpm listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, a))
}
