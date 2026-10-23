#!/usr/bin/env bash
# Extract the TLS ClientHello summary from a capture of the real Claude Code
# client talking to api.anthropic.com.
#
# Why: the fork ships a hand-written uTLS spec. Its provenance version is
# recorded in internal/antidetect/versionguard/profile.go (TLSHelloFrom). When
# that version differs from the advertised User-Agent version, every request
# advertises a client that never shipped. This script is how the spec gets
# re-derived from a real capture instead of guessed.
#
# Usage:
#   sudo tcpdump -i any -w /tmp/cc-cli.pcap 'host api.anthropic.com and tcp port 443'
#   # ... send one prompt from the real CLI, then Ctrl-C the tcpdump ...
#   tools/capture/ja4.sh /tmp/cc-cli.pcap
set -euo pipefail

PCAP="${1:?usage: ja4.sh <pcap>}"

if ! command -v tshark >/dev/null 2>&1; then
  echo "tshark not found. macOS: brew install wireshark (tshark is /opt/homebrew/bin/tshark)" >&2
  exit 1
fi

echo "== ClientHello per connection (ja4 / ja3_full / alpn / ciphers) =="
tshark -r "$PCAP" -Y 'tls.handshake.type==1' -T fields \
  -e tcp.stream \
  -e tls.handshake.ja4 \
  -e tls.handshake.ja3_full \
  -e tls.handshake.extensions_alpn_str \
  -e tls.handshake.ciphersuite \
  -E separator='|' 2>/dev/null | sort -u

echo
echo "== Extension order (must match the spec byte for byte) =="
tshark -r "$PCAP" -Y 'tls.handshake.type==1' -T fields \
  -e tcp.stream \
  -e tls.handshake.extensions_type 2>/dev/null | head -5

echo
echo "Next: compare with internal/runtime/executor/helps/utls_client.go"
echo "      and update TLSHelloFrom in internal/antidetect/versionguard/profile.go"
