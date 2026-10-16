package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

// MessageType identifies the type of signaling message exchanged over the TLS control channel.
type MessageType string

const (
	MsgCreateRoom       MessageType = "create_room"
	MsgJoinRoom         MessageType = "join_room"
	MsgUpdatePorts      MessageType = "update_ports"
	MsgUpdateCandidates MessageType = "update_candidates"
	MsgRequestPunch     MessageType = "request_punch"
	MsgPing             MessageType = "ping"

	MsgRoomJoined MessageType = "room_joined"
	MsgRoomState  MessageType = "room_state"
	MsgStartPunch MessageType = "start_punch"
	MsgError      MessageType = "error"
	MsgPong       MessageType = "pong"
)

// MaxSignalingMsgSize limits signaling JSON payloads to 1 MiB.
const MaxSignalingMsgSize = 1 << 20

// PortShare describes a local TCP or UDP port that a peer has explicitly shared with the room.
type PortShare struct {
	Protocol string `json:"protocol"` // "tcp" or "udp"
	Port     uint16 `json:"port"`
	Name     string `json:"name,omitempty"`
}

// Key returns a unique key for this protocol+port pair.
func (p PortShare) Key() string {
	return fmt.Sprintf("%s:%d", p.Protocol, p.Port)
}

// PeerInfo describes a participant in a room.
type PeerInfo struct {
	PeerID          string      `json:"peer_id"`
	Name            string      `json:"name"`
	Slot            uint8       `json:"slot"` // 1..250 -> mapped to 127.0.0.(slot+1)
	VirtualIP       string      `json:"virtual_ip"`
	CertFingerprint string      `json:"cert_fingerprint"`
	PublicEndpoint  string      `json:"public_endpoint,omitempty"`
	LocalCandidates []string    `json:"local_candidates,omitempty"`
	SharedPorts     []PortShare `json:"shared_ports,omitempty"`
}

// SlotToVirtualIP converts a 1-based peer room slot (1..250) into its 127.0.0.x loopback IP.
// Slot 1 -> 127.0.0.2, Slot 2 -> 127.0.0.3, etc.
func SlotToVirtualIP(slot uint8) string {
	return net.IPv4(127, 0, 0, slot+1).String()
}

// CreateRoomReq is sent by a client to create a new room on the EU server.
type CreateRoomReq struct {
	ServerSecretHash string      `json:"server_secret_hash,omitempty"`
	CustomCode       string      `json:"custom_code,omitempty"`
	PasswordHash     string      `json:"password_hash,omitempty"`
	PeerID           string      `json:"peer_id"`
	PeerName         string      `json:"peer_name"`
	CertFingerprint  string      `json:"cert_fingerprint"`
	LocalCandidates  []string    `json:"local_candidates,omitempty"`
	SharedPorts      []PortShare `json:"shared_ports,omitempty"`
}

// JoinRoomReq is sent by a client to join an existing room using a shared code.
type JoinRoomReq struct {
	ServerSecretHash string      `json:"server_secret_hash,omitempty"`
	RoomCode         string      `json:"room_code"`
	PasswordHash     string      `json:"password_hash,omitempty"`
	PeerID           string      `json:"peer_id"`
	PeerName         string      `json:"peer_name"`
	CertFingerprint  string      `json:"cert_fingerprint"`
	LocalCandidates  []string    `json:"local_candidates,omitempty"`
	SharedPorts      []PortShare `json:"shared_ports,omitempty"`
}

// UpdatePortsReq updates the list of ports shared by the sending peer.
type UpdatePortsReq struct {
	SharedPorts []PortShare `json:"shared_ports"`
}

// UpdateCandidatesReq updates the peer's local LAN UDP endpoints.
type UpdateCandidatesReq struct {
	LocalCandidates []string `json:"local_candidates"`
}

// RequestPunchReq asks the server to coordinate simultaneous UDP hole punching with TargetPeerID.
type RequestPunchReq struct {
	TargetPeerID string `json:"target_peer_id"`
}

// RoomJoinedResp is sent by the server when a client creates or joins a room.
type RoomJoinedResp struct {
	RoomCode      string     `json:"room_code"`
	RoomToken     uint64     `json:"room_token"` // 64-bit token authenticating UDP probe & relay packets
	Self          PeerInfo   `json:"self"`
	Peers         []PeerInfo `json:"peers"`
	ServerUDPPort int        `json:"server_udp_port"`
}

// RoomStateMsg is broadcast to all room members whenever peers or shared ports change.
type RoomStateMsg struct {
	RoomCode string     `json:"room_code"`
	Peers    []PeerInfo `json:"peers"`
}

// StartPunchMsg instructs a client to immediately punch UDP holes toward Peer.
type StartPunchMsg struct {
	Peer PeerInfo `json:"peer"`
}

// ErrorMsg reports a server-side error to the client.
type ErrorMsg struct {
	Message string `json:"message"`
}

// PingPongMsg is used for signaling keepalive and latency checks.
type PingPongMsg struct {
	Timestamp int64 `json:"timestamp"`
}

// Envelope wraps any signaling message with a type discriminator.
type Envelope struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// NewEnvelope marshals v into an Envelope with the given MessageType.
func NewEnvelope(msgType MessageType, v any) (Envelope, error) {
	if v == nil {
		return Envelope{Type: msgType}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal signaling payload (%s): %w", msgType, err)
	}
	return Envelope{
		Type:    msgType,
		Payload: raw,
	}, nil
}

// Decode unmarshals the envelope's JSON payload into dst.
func (e Envelope) Decode(dst any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("empty payload for message type %s", e.Type)
	}
	return json.Unmarshal(e.Payload, dst)
}

// WriteSignalingMsg writes a length-prefixed JSON Envelope to w.
func WriteSignalingMsg(w io.Writer, env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if len(data) > MaxSignalingMsgSize {
		return fmt.Errorf("signaling message too large: %d bytes", len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// ReadSignalingMsg reads a length-prefixed JSON Envelope from r.
func ReadSignalingMsg(r io.Reader) (Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Envelope{}, err
	}
	length := binary.BigEndian.Uint32(hdr[:])
	if length == 0 || length > MaxSignalingMsgSize {
		return Envelope{}, fmt.Errorf("invalid signaling message length: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return Envelope{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return env, nil
}
