package main

import (
	"fmt"
	"os"
	"time"

	"github.com/helzoph/chat/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func init() {
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
}

func makeServerAndStart(addr string) *server.Server {
	cfg := server.ServerConfig{
		ListenAddr: addr,
		Version:    "0.0.1",
	}
	s := server.NewServer(cfg)
	go s.Start()

	time.Sleep(300 * time.Millisecond)

	return s
}

func testMultipleServer(n int) {
	servers := make([]*server.Server, n)
	for i := 0; i < n; i++ {
		port := 3000 + i*1000
		addr := fmt.Sprintf(":%d", port)
		servers[i] = makeServerAndStart(addr)
	}

	for i := 0; i < n-1; i++ {
		time.Sleep(300 * time.Millisecond)
		servers[i+1].Connect(servers[i].ListenAddr)
	}

	time.Sleep(300 * time.Millisecond)
	if len(servers[0].Peers()) != n-1 {
		log.Fatal().Int("peer len", len(servers[0].Peers())).Strs("peers", servers[0].Peers()).Msg("peers len wrong")
	}
	servers[0].Broadcast("Hello, I'm first server")
}

func main() {
	testMultipleServer(4)

	select {}
}
