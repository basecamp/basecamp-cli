package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	mode := flag.String("mode", "replay", "Operating mode: record, replay")
	target := flag.String("target", "", "Target URL (required for record mode)")
	cassDir := flag.String("cassettes", ".", "Cassette directory")
	portFile := flag.String("port-file", "", "Write listen port to this file")
	account := flag.String("account", "", "Account label for cassette metadata")
	flag.Parse()

	lc := net.ListenConfig{}
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		log.Fatal("unexpected listener address type")
	}
	port := tcpAddr.Port
	proxyHost := fmt.Sprintf("http://127.0.0.1:%d", port)
	log.Printf("listening on %s (mode=%s)", proxyHost, *mode)

	if *portFile != "" {
		if err := os.WriteFile(*portFile, []byte(fmt.Sprintf("%d", port)), 0o600); err != nil {
			log.Fatalf("writing port file: %v", err)
		}
	}

	var handler http.Handler

	// Both modes trap signals for clean shutdown. Record mode saves
	// cassettes; replay mode just exits 0 (killed by stop_proxy).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	switch *mode {
	case "replay":
		rs, err := newReplayServer(*cassDir)
		if err != nil {
			log.Fatalf("loading cassettes: %v", err)
		}
		rs.proxyHost = proxyHost
		handler = rs

		go func() {
			<-sig
			os.Exit(0)
		}()

	case "record":
		if *target == "" {
			log.Fatal("-target required in record mode")
		}
		rp := newRecordingProxy(*target, *cassDir, *account, proxyHost)
		handler = rp

		go func() {
			<-sig
			if err := rp.save(); err != nil {
				log.Printf("error saving cassettes: %v", err)
				os.Exit(1)
			}
			os.Exit(0)
		}()

	default:
		log.Fatalf("unknown mode: %s", *mode)
	}

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	log.Fatal(srv.Serve(ln))
}
