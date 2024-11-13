package server

type Message struct {
	From string
	Body any
}

type Handshake struct {
	Version    string
	ListenAddr string
}

type MessagePeerList struct {
	Peers []string
}
