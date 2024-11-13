package server

import (
	"bytes"
	"context"
	"encoding/gob"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"
)

func init() {
	gob.Register(MessagePeerList{})
	gob.Register(Handshake{})
}

type ServerConfig struct {
	ListenAddr string
	Version    string
}

type Server struct {
	ServerConfig

	listener *quic.Listener
	peers    map[net.Addr]*Peer

	addPeer chan *Peer
	delPeer chan *Peer
	msgCh   chan *Message
	addrCh  chan string

	peerLock sync.RWMutex
}

func NewServer(config ServerConfig) *Server {
	return &Server{
		ServerConfig: config,
		peers:        make(map[net.Addr]*Peer),
		addPeer:      make(chan *Peer, 10),
		delPeer:      make(chan *Peer),
		msgCh:        make(chan *Message, 10),
		addrCh:       make(chan string, 10),
	}
}

func (s *Server) Start() {
	go s.loop()
	s.listenAndAccept()
}

func (s *Server) listenAndAccept() {
	ln, err := quic.ListenAddr(s.ListenAddr, generateTLSConfig(), nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to listen")
	}
	s.listener = ln
	log.Info().Str("addr", s.ListenAddr).Msg("Listening")

	ctx := context.Background()

	for {
		conn, err := s.listener.Accept(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Failed to accept")
			continue
		}

		peer, err := NewPeer(conn, false)
		if err != nil {
			log.Error().Err(err).Msg("Failed to create peer")
			continue
		}

		s.addPeer <- peer
	}
}

func (s *Server) loop() {
	for {
		select {
		case peer := <-s.addPeer:
			if err := s.handleNewPeer(peer); err != nil {
				log.Error().Err(err)
			}

		case peer := <-s.delPeer:
			log.Info().Str("peer", peer.listenAddr).Msg("Peer disconnected")
			delete(s.peers, peer.conn.RemoteAddr())
			peer.Close()

		case msg := <-s.msgCh:
			if err := s.handleMessage(msg); err != nil {
				log.Error().Err(err).Msg("Failed to handle message")
			}
		}
	}
}

func (s *Server) handleNewPeer(peer *Peer) error {
	if err := s.handshake(peer); err != nil {
		log.Error().Err(err).Str("peer", peer.listenAddr).Msg("Failed to handshake")
		s.delPeer <- peer
		return err
	}

	go peer.ReadLoop(s.delPeer, s.msgCh)

	if !peer.outbound {
		if err := s.sendHandshake(peer); err != nil {
			log.Error().Err(err).Str("peer", peer.listenAddr).Msg("Failed to send handshake")
			s.delPeer <- peer
			return err
		}

		go func() {
			if err := s.sendPeerList(peer); err != nil {
				log.Error().Err(err).Str("peer", peer.listenAddr).Msg("Failed to send peer list")
			}
		}()
	}
	log.Info().Msgf("Handshake completed: %s -> %s", peer.listenAddr, s.ListenAddr)

	s.addPeers(peer)

	return nil
}

func (s *Server) handshake(peer *Peer) error {
	hs := &Handshake{}

	if err := gob.NewDecoder(peer.stream).Decode(hs); err != nil {
		return err
	}

	if s.Version != hs.Version {
		return ErrVersionMismatch
	}

	peer.listenAddr = hs.ListenAddr

	return nil
}

func (s *Server) sendHandshake(peer *Peer) error {
	msg := &Handshake{
		Version:    s.Version,
		ListenAddr: s.ListenAddr,
	}

	buf := new(bytes.Buffer)
	for retries := 0; retries < 3; retries++ {
		err := gob.NewEncoder(buf).Encode(msg)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return peer.Send(buf.Bytes())
}

func (s *Server) handleMessage(msg *Message) error {
	switch m := msg.Body.(type) {
	case MessagePeerList:
		return s.handlePeerList(m)
	}

	return nil
}

func (s *Server) Connect(addr string) error {
	log.Debug().Msgf("Connecting: %s -> %s", s.ListenAddr, addr)

	if s.isInPeerList(addr) {
		return nil
	}

	tlsConf := generateTLSConfig()

	conn, err := quic.DialAddr(context.Background(), addr, tlsConf, nil)
	if err != nil {
		log.Error().Err(err).Str("addr", addr).Msg("Failed to connect")
		return err
	}

	peer, err := NewPeer(conn, true)
	if err != nil {
		log.Error().Err(err).Str("addr", addr).Msg("Failed to create peer")
		return err
	}

	// Make sure the peer is added before sending handshake
	// TODO this is a hack. Find a better way to handle this
	time.Sleep(100 * time.Millisecond)

	s.addPeer <- peer

	return s.sendHandshake(peer)
}

func (s *Server) addPeers(peer *Peer) {
	s.peerLock.Lock()
	defer s.peerLock.Unlock()

	s.peers[peer.conn.RemoteAddr()] = peer

	log.Debug().Str("from", s.ListenAddr).Str("peer", peer.listenAddr).Msg("Peer added")
}

func (s *Server) Peers() []string {
	s.peerLock.RLock()
	defer s.peerLock.RUnlock()

	peers := make([]string, len(s.peers))
	it := 0
	for _, peer := range s.peers {
		peers[it] = peer.listenAddr
		it++
	}

	return peers
}

func (s *Server) sendPeerList(peer *Peer) error {
	peerList := MessagePeerList{
		Peers: []string{},
	}
	peers := s.Peers()

	for i := 0; i < len(peers); i++ {
		if peers[i] != peer.listenAddr {
			peerList.Peers = append(peerList.Peers, peers[i])
		}
	}

	if len(peerList.Peers) == 0 {
		return nil
	}

	msg := &Message{
		From: s.ListenAddr,
		Body: peerList,
	}
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(msg); err != nil {
		return err
	}

	log.Debug().Strs("peers", peers).Msgf("Sending peer list: %s -> %s", s.ListenAddr, peer.listenAddr)

	return peer.Send(buf.Bytes())
}

func (s *Server) handlePeerList(pl MessagePeerList) error {
	log.Debug().Strs("peers", pl.Peers).Str("to", s.ListenAddr).Msg("Received peer list")

	for i := 0; i < len(pl.Peers); i++ {
		if err := s.Connect(pl.Peers[i]); err != nil {
			log.Error().Err(err).Str("addr", pl.Peers[i]).Msg("Failed to connect")
		}
	}

	return nil
}

func (s *Server) isInPeerList(addr string) bool {
	peers := s.Peers()
	for i := 0; i < len(peers); i++ {
		if peers[i] == addr {
			return true
		}
	}
	return false
}

func (s *Server) Broadcast(broadcastMsg string) {
	msg := &Message{
		From: s.ListenAddr,
		Body: broadcastMsg,
	}

	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(msg); err != nil {
		log.Error().Err(err).Msg("Failed to encode message")
		return
	}

	for _, peer := range s.peers {
		go func(peer *Peer) {
			if err := peer.Send(buf.Bytes()); err != nil {
				log.Error().Err(err).Str("peer", peer.listenAddr).Msg("Failed to broadcast")
			}
			log.Info().Str("to", peer.listenAddr).Str("msg", broadcastMsg).Msg("Broadcast message")
		}(peer)
	}
}
