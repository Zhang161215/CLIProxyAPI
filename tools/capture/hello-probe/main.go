// Command hello-probe records the TLS ClientHello of whoever connects, without
// decrypting anything and without root.
//
// The fork ships a hand-written uTLS ClientHello spec. Whether that spec is
// still current cannot be answered from an HTTP capture: mitmproxy terminates
// TLS, so what it sees is its own handshake. This probe reads the raw bytes the
// client sends before any certificate is checked, which is enough to compare
// cipher suites, extension order and ALPN against the spec.
//
// Usage:
//
//	go run ./tools/capture/hello-probe -addr 127.0.0.1:9443 -n 1
//	ANTHROPIC_BASE_URL=https://127.0.0.1:9443 claude -p "x"   # the request will fail; only the hello matters
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9443", "listen address")
	count := flag.Int("n", 1, "number of hellos to capture before exiting")
	timeout := flag.Duration("timeout", 60*time.Second, "overall deadline")
	flag.Parse()

	listener, errListen := net.Listen("tcp", *addr)
	if errListen != nil {
		fmt.Fprintf(os.Stderr, "hello-probe: listen: %v\n", errListen)
		os.Exit(2)
	}
	defer func() {
		if errClose := listener.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "hello-probe: close: %v\n", errClose)
		}
	}()

	deadline := time.Now().Add(*timeout)
	captured := 0
	for captured < *count {
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "hello-probe: timeout after %d hello(s)\n", captured)
			os.Exit(1)
		}
		conn, errAccept := listener.(*net.TCPListener).AcceptTCP()
		if errAccept != nil {
			fmt.Fprintf(os.Stderr, "hello-probe: accept: %v\n", errAccept)
			continue
		}
		hello, errRead := readClientHello(conn, 5*time.Second)
		_ = conn.Close()
		if errRead != nil {
			fmt.Fprintf(os.Stderr, "hello-probe: read: %v\n", errRead)
			continue
		}
		captured++
		fmt.Printf("=== hello %d from %s ===\n", captured, conn.RemoteAddr())
		fmt.Print(hello)
	}
}

// readClientHello parses the first TLS record far enough to describe it.
func readClientHello(conn net.Conn, timeout time.Duration) (string, error) {
	if errDeadline := conn.SetReadDeadline(time.Now().Add(timeout)); errDeadline != nil {
		return "", errDeadline
	}
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(conn, header); errRead != nil {
		return "", errRead
	}
	if header[0] != 0x16 {
		return "", fmt.Errorf("not a handshake record (type 0x%02x)", header[0])
	}
	length := int(binary.BigEndian.Uint16(header[3:5]))
	body := make([]byte, length)
	if _, errRead := io.ReadFull(conn, body); errRead != nil {
		return "", errRead
	}
	if len(body) < 4 || body[0] != 0x01 {
		return "", fmt.Errorf("not a ClientHello")
	}
	payload := body[4:] // skip handshake type + length

	if len(payload) < 34 {
		return "", fmt.Errorf("truncated ClientHello")
	}
	pos := 2 + 32 // legacy version + random

	// session_id
	if pos >= len(payload) {
		return "", fmt.Errorf("truncated at session id")
	}
	sessionLen := int(payload[pos])
	pos += 1 + sessionLen

	// cipher suites
	if pos+2 > len(payload) {
		return "", fmt.Errorf("truncated at cipher suites")
	}
	cipherLen := int(binary.BigEndian.Uint16(payload[pos : pos+2]))
	pos += 2
	if pos+cipherLen > len(payload) {
		return "", fmt.Errorf("truncated cipher list")
	}
	var ciphers []string
	for i := 0; i+1 < cipherLen; i += 2 {
		ciphers = append(ciphers, fmt.Sprintf("0x%04x", binary.BigEndian.Uint16(payload[pos+i:pos+i+2])))
	}
	pos += cipherLen

	// compression methods
	if pos >= len(payload) {
		return "", fmt.Errorf("truncated at compression")
	}
	compressionLen := int(payload[pos])
	pos += 1 + compressionLen

	var extensions []string
	var alpn, sni string
	if pos+2 <= len(payload) {
		extTotal := int(binary.BigEndian.Uint16(payload[pos : pos+2]))
		pos += 2
		end := pos + extTotal
		if end > len(payload) {
			end = len(payload)
		}
		for pos+4 <= end {
			extType := binary.BigEndian.Uint16(payload[pos : pos+2])
			extLen := int(binary.BigEndian.Uint16(payload[pos+2 : pos+4]))
			data := payload[pos+4:]
			if len(data) > extLen {
				data = data[:extLen]
			}
			extensions = append(extensions, fmt.Sprintf("%d(%s)", extType, extensionName(extType)))
			switch extType {
			case 0:
				sni = parseSNI(data)
			case 16:
				alpn = parseALPN(data)
			}
			pos += 4 + extLen
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "cipher_suites(%d): %s\n", len(ciphers), strings.Join(ciphers, ","))
	fmt.Fprintf(&builder, "extensions(%d): %s\n", len(extensions), strings.Join(extensions, ","))
	fmt.Fprintf(&builder, "sni: %s\n", orNone(sni))
	fmt.Fprintf(&builder, "alpn: %s\n", orNone(alpn))
	fmt.Fprintf(&builder, "raw_hex_head: %s\n", hex.EncodeToString(payload[:min(len(payload), 64)]))
	return builder.String(), nil
}

func extensionName(extType uint16) string {
	names := map[uint16]string{
		0: "server_name", 5: "status_request", 10: "supported_groups", 11: "ec_point_formats",
		13: "signature_algorithms", 16: "alpn", 18: "sct", 21: "padding", 23: "extended_master_secret",
		35: "session_ticket", 41: "pre_shared_key", 43: "supported_versions", 45: "psk_key_exchange_modes",
		51: "key_share", 65281: "renegotiation_info",
	}
	if name, ok := names[extType]; ok {
		return name
	}
	return "?"
}

func parseSNI(data []byte) string {
	// server_name_list: 2-byte list length, then entries
	if len(data) < 5 {
		return ""
	}
	entryLen := int(binary.BigEndian.Uint16(data[3:5]))
	if len(data) < 5+entryLen {
		return ""
	}
	return string(data[5 : 5+entryLen])
}

func parseALPN(data []byte) string {
	if len(data) < 3 {
		return ""
	}
	protoLen := int(data[2])
	if len(data) < 3+protoLen {
		return ""
	}
	return string(data[3 : 3+protoLen])
}

func orNone(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
