package server

import (
	"context"
	"encoding/gob"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"
)

type Peer struct {
	listenAddr string
	outbound   bool
	conn       quic.Connection
	stream     quic.Stream
}

func NewPeer(conn quic.Connection, outbound bool) (*Peer, error) {
	var stream quic.Stream
	var err error

	// Streams are created asynchronously and may not be fully established before the message is sent.
	if outbound {
		// Client actively opens stream
		stream, err = conn.OpenStream()
	} else {
		// Server waits to receive stream
		stream, err = conn.AcceptStream(context.Background())
	}

	if err != nil {
		return nil, err
	}

	return &Peer{
		conn:       conn,
		stream:     stream,
		outbound:   outbound,
		listenAddr: conn.RemoteAddr().String(),
	}, nil
}

func (p *Peer) Close() error {
	if p.stream != nil {
		p.stream.Close()
	}
	return p.conn.CloseWithError(0, "closed")
}

func (p *Peer) Send(b []byte) error {
	_, err := p.stream.Write(b)
	return err
}

func (p *Peer) ReadLoop(delPeer chan *Peer, msgCh chan *Message) {
	for {
		msg := &Message{}
		if err := gob.NewDecoder(p.stream).Decode(msg); err != nil {
			log.Error().Err(err).Str("addr", p.listenAddr).Msgf("Failed to decode message: %+v", msg)
			break
		}

		msgCh <- msg
	}

	delPeer <- p
}
