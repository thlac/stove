package main

import (
	"flag"
	"fmt"
	"github.com/HearthSim/stove/bnet"
	"github.com/HearthSim/stove/pegasus"
	_ "github.com/rakyll/gom/http"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const (
	CONN_DEFAULT_HOST = "localhost"
	CONN_DEFAULT_PORT = 1119
)

type BroadcastWriter struct {
	Writers []io.Writer
}

func (b *BroadcastWriter) Write(p []byte) (n int, err error) {
	for _, w := range b.Writers {
		n, err = w.Write(p)
		if err != nil {
			return
		}
	}
	return
}

func main() {
	addr := fmt.Sprintf("%s:%d", CONN_DEFAULT_HOST, CONN_DEFAULT_PORT)
	flag.StringVar(&addr, "bind", addr, "The address to run on")
	runMigrate := flag.Bool("migrate", false, "Perform a database migration and exit")
	flag.Parse()

	if !strings.Contains(addr, ":") {
		addr = fmt.Sprintf("%s:%d", addr, CONN_DEFAULT_PORT)
	}

	if *runMigrate {
		fmt.Printf("Performing database migration\n")
		pegasus.Migrate()
		return
	}

	logFile, _ := os.Create("stove.log")
	log.SetOutput(&BroadcastWriter{
		[]io.Writer{
			os.Stdout,
			logFile,
		},
	})
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	go func() {
		httpAddr := "localhost:6060"
		log.Printf("Debug http server listening on %s ...\n", httpAddr)
		log.Println(http.ListenAndServe(httpAddr, nil))
	}()

	serv := bnet.NewServer()
	serv.RegisterGameServer("WTCG", pegasus.NewServer(serv))

	log.Printf("Listening on %s ...\n", addr)
	err := serv.ListenAndServe(addr)
	if err != nil {
		log.Println(err)
	}
}
