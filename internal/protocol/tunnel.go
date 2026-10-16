package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// UDP Magic headers (4 bytes each).
var (
	MagicProbe    = [4]byte{'P', '2', 'P', 'B'} // Client -> EU Server UDP probe: [4B "P2PB"][8B RoomToken][1B SrcSlot]
	MagicProbeAck = [4]byte{'P', '2', 'P', 'A'} // EU Server -> Client UDP probe ack: [4B "P2PA"][8B RoomToken][2B Len][PublicAddr string]
	MagicPunch    = [4]byte{'P', '2', 'P', 'H'} // Peer <-> Peer UDP hole punch: [4B "P2PH"][8B RoomToken][1B Kind (0=ping,1=pong)][1B SrcSlot]
	MagicRelay    = [4]byte{'P', '2', 'P', 'R'} // Client <-> EU Server Relay wrapper: [4B "P2PR"][8B RoomToken][1B SrcSlot][1B DstSlot][QUIC payload...]
)

const (
	RelayHeaderSize = 14 // 4B magic + 8B token + 1B srcSlot + 1B dstSlot
	ProbePacketSize = 13 // 4B magic + 8B token + 1B srcSlot
	PunchPacketSize = 14 // 4B magic + 8B token + 1B kind + 1B srcSlot

	PunchKindPing uint8 = 0
	PunchKindPong uint8 = 1

	RelaySyntheticPort = 4433
)

// QUIC Stream Types.
const (
	StreamTypePing uint8 = 0x01
	StreamTypeTCP  uint8 = 0x02
	StreamTypeUDP  uint8 = 0x03

	StreamStatusOK  uint8 = 0x00
	StreamStatusErr uint8 = 0x01

	MaxUDPFrameSize = 65507
)

// RelayUDPAddr returns the synthetic *net.UDPAddr (127.254.0.<peerSlot>:4433) representing
// a QUIC connection routed through the EU server's UDP relay for peerSlot.
func RelayUDPAddr(peerSlot uint8) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(127, 254, 0, peerSlot),
		Port: RelaySyntheticPort,
	}
}

// ParseRelayUDPAddr checks whether addr is a synthetic EU relay address (127.254.0.<slot>)
// and returns the peer slot if so.
func ParseRelayUDPAddr(addr net.Addr) (uint8, bool) {
	uAddr, ok := addr.(*net.UDPAddr)
	if !ok || uAddr == nil {
		return 0, false
	}
	ip4 := uAddr.IP.To4()
	if ip4 == nil {
		return 0, false
	}
	if ip4[0] == 127 && ip4[1] == 254 && ip4[2] == 0 && ip4[3] >= 1 {
		return ip4[3], true
	}
	return 0, false
}

// EncodeProbePacket builds a 13-byte UDP endpoint discovery probe sent to the EU server.
func EncodeProbePacket(roomToken uint64, srcSlot uint8) []byte {
	buf := make([]byte, ProbePacketSize)
	copy(buf[0:4], MagicProbe[:])
	binary.BigEndian.PutUint64(buf[4:12], roomToken)
	buf[12] = srcSlot
	return buf
}

// DecodeProbePacket parses a 13-byte UDP endpoint discovery probe.
func DecodeProbePacket(b []byte) (roomToken uint64, srcSlot uint8, ok bool) {
	if len(b) < ProbePacketSize ||
		b[0] != MagicProbe[0] || b[1] != MagicProbe[1] || b[2] != MagicProbe[2] || b[3] != MagicProbe[3] {
		return 0, 0, false
	}
	roomToken = binary.BigEndian.Uint64(b[4:12])
	srcSlot = b[12]
	return roomToken, srcSlot, true
}

// EncodeProbeAckPacket builds a UDP probe acknowledgment containing the client's observed public IP:port.
func EncodeProbeAckPacket(roomToken uint64, publicEndpoint string) []byte {
	epBytes := []byte(publicEndpoint)
	buf := make([]byte, 14+len(epBytes))
	copy(buf[0:4], MagicProbeAck[:])
	binary.BigEndian.PutUint64(buf[4:12], roomToken)
	binary.BigEndian.PutUint16(buf[12:14], uint16(len(epBytes)))
	copy(buf[14:], epBytes)
	return buf
}

// DecodeProbeAckPacket parses a UDP probe acknowledgment packet.
func DecodeProbeAckPacket(b []byte) (roomToken uint64, publicEndpoint string, ok bool) {
	if len(b) < 14 ||
		b[0] != MagicProbeAck[0] || b[1] != MagicProbeAck[1] || b[2] != MagicProbeAck[2] || b[3] != MagicProbeAck[3] {
		return 0, "", false
	}
	roomToken = binary.BigEndian.Uint64(b[4:12])
	epLen := int(binary.BigEndian.Uint16(b[12:14]))
	if len(b) < 14+epLen {
		return 0, "", false
	}
	return roomToken, string(b[14 : 14+epLen]), true
}

// EncodePunchPacket builds a 14-byte peer-to-peer UDP hole-punch ping or pong packet.
func EncodePunchPacket(roomToken uint64, kind uint8, srcSlot uint8) []byte {
	buf := make([]byte, PunchPacketSize)
	copy(buf[0:4], MagicPunch[:])
	binary.BigEndian.PutUint64(buf[4:12], roomToken)
	buf[12] = kind
	buf[13] = srcSlot
	return buf
}

// DecodePunchPacket parses a 14-byte peer-to-peer UDP hole-punch packet.
func DecodePunchPacket(b []byte) (roomToken uint64, kind uint8, srcSlot uint8, ok bool) {
	if len(b) < PunchPacketSize ||
		b[0] != MagicPunch[0] || b[1] != MagicPunch[1] || b[2] != MagicPunch[2] || b[3] != MagicPunch[3] {
		return 0, 0, 0, false
	}
	roomToken = binary.BigEndian.Uint64(b[4:12])
	return roomToken, b[12], b[13], true
}

// EncodeRelayPacket wraps an encrypted QUIC packet inside a 14-byte relay header for the EU UDP relay.
func EncodeRelayPacket(roomToken uint64, srcSlot, dstSlot uint8, payload []byte) []byte {
	buf := make([]byte, RelayHeaderSize+len(payload))
	copy(buf[0:4], MagicRelay[:])
	binary.BigEndian.PutUint64(buf[4:12], roomToken)
	buf[12] = srcSlot
	buf[13] = dstSlot
	copy(buf[RelayHeaderSize:], payload)
	return buf
}

// DecodeRelayPacket unwraps a 14-byte relay header and returns the metadata and inner QUIC payload slice.
func DecodeRelayPacket(b []byte) (roomToken uint64, srcSlot, dstSlot uint8, payload []byte, ok bool) {
	if len(b) <= RelayHeaderSize ||
		b[0] != MagicRelay[0] || b[1] != MagicRelay[1] || b[2] != MagicRelay[2] || b[3] != MagicRelay[3] {
		return 0, 0, 0, nil, false
	}
	roomToken = binary.BigEndian.Uint64(b[4:12])
	srcSlot = b[12]
	dstSlot = b[13]
	return roomToken, srcSlot, dstSlot, b[RelayHeaderSize:], true
}

// MuxPacketConn wraps a *net.UDPConn so that a single UDP socket can simultaneously:
//  1. Carry direct peer-to-peer QUIC packets.
//  2. Send and receive STUN-like UDP endpoint discovery probes ("P2PB" / "P2PA").
//  3. Send and receive UDP hole-punch ping/pong packets ("P2PH").
//  4. Transparently wrap/unwrap QUIC packets routed via the EU UDP relay ("P2PR") using synthetic 127.254.0.x addresses.
type MuxPacketConn struct {
	 net.PacketConn

	mu            sync.RWMutex
	roomToken     uint64
	selfSlot      uint8
	serverUDPAddr *net.UDPAddr
	onProbeAck    func(publicEndpoint string)
	onPunch       func(peerSlot uint8, addr *net.UDPAddr)
}

// NewMuxPacketConn creates a new MuxPacketConn wrapping conn.
func NewMuxPacketConn(conn net.PacketConn) *MuxPacketConn {
	return &MuxPacketConn{
		PacketConn: conn,
	}
}

// ConfigureRoom updates the room relay metadata and callbacks for this socket.
func (m *MuxPacketConn) ConfigureRoom(
	roomToken uint64,
	selfSlot uint8,
	serverUDPAddr *net.UDPAddr,
	onProbeAck func(publicEndpoint string),
	onPunch func(peerSlot uint8, addr *net.UDPAddr),
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roomToken = roomToken
	m.selfSlot = selfSlot
	m.serverUDPAddr = serverUDPAddr
	m.onProbeAck = onProbeAck
	m.onPunch = onPunch
}

// SendRaw writes a raw UDP packet bypassing synthetic relay wrapping.
func (m *MuxPacketConn) SendRaw(b []byte, addr net.Addr) error {
	_, err := m.PacketConn.WriteTo(b, addr)
	return err
}

// SetReadBuffer forwards UDP receive buffer sizing to the underlying *net.UDPConn if supported.
func (m *MuxPacketConn) SetReadBuffer(bytes int) error {
	if uc, ok := m.PacketConn.(interface{ SetReadBuffer(int) error }); ok {
		return uc.SetReadBuffer(bytes)
	}
	return nil
}

// SetWriteBuffer forwards UDP send buffer sizing to the underlying *net.UDPConn if supported.
func (m *MuxPacketConn) SetWriteBuffer(bytes int) error {
	if uc, ok := m.PacketConn.(interface{ SetWriteBuffer(int) error }); ok {
		return uc.SetWriteBuffer(bytes)
	}
	return nil
}

// ReadFrom intercepts P2PA, P2PH, and P2PR packets and passes QUIC packets to quic.Transport.
func (m *MuxPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	var scratch [2048]byte
	for {
		n, addr, err := m.PacketConn.ReadFrom(scratch[:])
		if err != nil {
			return 0, nil, err
		}
		pkt := scratch[:n]

		// 1. Check for EU Server Probe Ack ("P2PA")
		if token, pubEP, ok := DecodeProbeAckPacket(pkt); ok {
			m.mu.RLock()
			expectedToken := m.roomToken
			cb := m.onProbeAck
			m.mu.RUnlock()
			if expectedToken != 0 && token == expectedToken && cb != nil {
				cb(pubEP)
			}
			continue
		}

		// 2. Check for Peer Hole-Punch Ping/Pong ("P2PH")
		if token, kind, peerSlot, ok := DecodePunchPacket(pkt); ok {
			m.mu.RLock()
			expectedToken := m.roomToken
			selfSlot := m.selfSlot
			cb := m.onPunch
			m.mu.RUnlock()
			if expectedToken != 0 && token == expectedToken && peerSlot != selfSlot {
				if kind == PunchKindPing {
					pong := EncodePunchPacket(expectedToken, PunchKindPong, selfSlot)
					_, _ = m.PacketConn.WriteTo(pong, addr)
				}
				if uAddr, ok := addr.(*net.UDPAddr); ok && cb != nil {
					cb(peerSlot, uAddr)
				}
			}
			continue
		}

		// 3. Check for EU Relayed QUIC Packet ("P2PR")
		if token, srcSlot, dstSlot, inner, ok := DecodeRelayPacket(pkt); ok {
			m.mu.RLock()
			expectedToken := m.roomToken
			selfSlot := m.selfSlot
			m.mu.RUnlock()
			if expectedToken != 0 && token == expectedToken && dstSlot == selfSlot && srcSlot != 0 {
				copied := copy(p, inner)
				return copied, RelayUDPAddr(srcSlot), nil
			}
			continue
		}

		// 4. Ignore stray probe packets if any
		if _, _, ok := DecodeProbePacket(pkt); ok {
			continue
		}

		// 5. Standard direct QUIC packet
		copied := copy(p, pkt)
		return copied, addr, nil
	}
}

// WriteTo transparently wraps packets addressed to synthetic RelayUDPAddr (127.254.0.x)
// and sends them through the EU server's UDP relay.
func (m *MuxPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if dstSlot, isRelay := ParseRelayUDPAddr(addr); isRelay {
		m.mu.RLock()
		token := m.roomToken
		selfSlot := m.selfSlot
		srvAddr := m.serverUDPAddr
		m.mu.RUnlock()
		if srvAddr == nil || token == 0 || selfSlot == 0 {
			return 0, errors.New("relay not configured")
		}
		wrapped := EncodeRelayPacket(token, selfSlot, dstSlot, p)
		if _, err := m.PacketConn.WriteTo(wrapped, srvAddr); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return m.PacketConn.WriteTo(p, addr)
}

// WriteStreamOpenHeader writes the 3-byte stream header ([1B StreamType][2B Port]) used to open a TCP or UDP tunnel stream.
func WriteStreamOpenHeader(w io.Writer, streamType uint8, port uint16) error {
	var hdr [3]byte
	hdr[0] = streamType
	binary.BigEndian.PutUint16(hdr[1:3], port)
	_, err := w.Write(hdr[:])
	return err
}

// ReadStreamOpenPort reads the 2-byte target port after the stream type byte has been read.
func ReadStreamOpenPort(r io.Reader) (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(buf[:]), nil
}

// WriteStreamStatus writes the stream handshake response ([1B Status] + optional [2B ErrLen][ErrMsg]).
func WriteStreamStatus(w io.Writer, status uint8, errMsg string) error {
	if status == StreamStatusOK {
		_, err := w.Write([]byte{StreamStatusOK})
		return err
	}
	msgBytes := []byte(errMsg)
	if len(msgBytes) > 1024 {
		msgBytes = msgBytes[:1024]
	}
	buf := make([]byte, 3+len(msgBytes))
	buf[0] = status
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(msgBytes)))
	copy(buf[3:], msgBytes)
	_, err := w.Write(buf)
	return err
}

// ReadStreamStatus reads the stream handshake response from r.
func ReadStreamStatus(r io.Reader) error {
	var status [1]byte
	if _, err := io.ReadFull(r, status[:]); err != nil {
		return err
	}
	if status[0] == StreamStatusOK {
		return nil
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return fmt.Errorf("remote rejected stream (status %d)", status[0])
	}
	msgLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if msgLen == 0 {
		return fmt.Errorf("remote rejected stream (status %d)", status[0])
	}
	msgBuf := make([]byte, msgLen)
	if _, err := io.ReadFull(r, msgBuf); err != nil {
		return fmt.Errorf("remote rejected stream (status %d)", status[0])
	}
	return fmt.Errorf("remote peer error: %s", string(msgBuf))
}

// WriteUDPFrame writes a length-prefixed UDP datagram over a QUIC stream.
func WriteUDPFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxUDPFrameSize {
		return fmt.Errorf("udp payload too large: %d", len(payload))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadUDPFrame reads a length-prefixed UDP datagram from a QUIC stream into buf.
func ReadUDPFrame(r io.Reader, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	length := int(binary.BigEndian.Uint16(hdr[:]))
	if length > len(buf) {
		return 0, fmt.Errorf("udp frame size %d exceeds buffer %d", length, len(buf))
	}
	if _, err := io.ReadFull(r, buf[:length]); err != nil {
		return 0, err
	}
	return length, nil
}
