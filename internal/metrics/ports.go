package metrics

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/wf-pro-dev/tailkit/types"
	"tailscale.com/portlist"
)

type portSnapshotter interface {
	Snapshot(context.Context) ([]types.Port, error)
}

type procPortSnapshotter struct {
	procRoot string
	poller   portlistPoller
	cached   []portlist.Port
}

func newProcPortSnapshotter(procRoot string) *procPortSnapshotter {
	return &procPortSnapshotter{
		procRoot: procRoot,
		poller: &portlist.Poller{
			IncludeLocalhost: true,
		},
	}
}

func (p *procPortSnapshotter) Snapshot(_ context.Context) ([]types.Port, error) {
	sockets, err := p.readSockets()
	if err != nil {
		return nil, err
	}

	ports, err := p.poll()
	if err != nil {
		return nil, err
	}
	if len(ports) == 0 {
		return nil, nil
	}

	snapshot := p.buildSnapshot(ports, sockets)

	sort.Slice(snapshot, func(i, j int) bool {
		if snapshot[i].Port != snapshot[j].Port {
			return snapshot[i].Port < snapshot[j].Port
		}
		if snapshot[i].Addr != snapshot[j].Addr {
			return snapshot[i].Addr < snapshot[j].Addr
		}
		if snapshot[i].Proto != snapshot[j].Proto {
			return snapshot[i].Proto < snapshot[j].Proto
		}
		if snapshot[i].PID != snapshot[j].PID {
			return snapshot[i].PID < snapshot[j].PID
		}
		return snapshot[i].Process < snapshot[j].Process
	})
	return snapshot, nil
}

type portlistPoller interface {
	Poll() ([]portlist.Port, bool, error)
}

type socketRow struct {
	addr  string
	port  uint16
	proto string
	inode string
}

type procNetFile struct {
	name       string
	proto      string
	ipv6       bool
	remoteAny  string
	listenOnly bool
}

func (p *procPortSnapshotter) readSockets() ([]socketRow, error) {
	files := []procNetFile{
		{name: "tcp", proto: "tcp", remoteAny: "00000000:0000", listenOnly: true},
		{name: "tcp6", proto: "tcp", ipv6: true, remoteAny: "00000000000000000000000000000000:0000", listenOnly: true},
		{name: "udp", proto: "udp", remoteAny: "00000000:0000"},
		{name: "udp6", proto: "udp", ipv6: true, remoteAny: "00000000000000000000000000000000:0000"},
	}

	var sockets []socketRow
	for _, file := range files {
		rows, err := p.readSocketFile(filepath.Join(p.procRoot, "net", file.name), file)
		if err != nil {
			return nil, err
		}
		sockets = append(sockets, rows...)
	}
	return sockets, nil
}

func (p *procPortSnapshotter) readSocketFile(path string, cfg procNetFile) ([]socketRow, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var rows []socketRow
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}

		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		if cfg.listenOnly && fields[3] != "0A" {
			continue
		}
		if fields[2] != cfg.remoteAny {
			continue
		}

		addr, port, err := decodeSocketAddress(fields[1], cfg.ipv6)
		if err != nil {
			continue
		}
		rows = append(rows, socketRow{
			addr:  addr,
			port:  port,
			proto: cfg.proto,
			inode: fields[9],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func decodeSocketAddress(value string, ipv6 bool) (string, uint16, error) {
	host, portHex, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, fmt.Errorf("invalid socket address %q", value)
	}

	port64, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, err
	}

	ip, err := decodeProcIP(host, ipv6)
	if err != nil {
		return "", 0, err
	}
	return ip, uint16(port64), nil
}

func decodeProcIP(value string, ipv6 bool) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return "", err
	}

	if !ipv6 {
		if len(raw) != net.IPv4len {
			return "", fmt.Errorf("invalid ipv4 length %d", len(raw))
		}
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String(), nil
	}

	if len(raw) != net.IPv6len {
		return "", fmt.Errorf("invalid ipv6 length %d", len(raw))
	}
	decoded := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 4 {
		decoded[i] = raw[i+3]
		decoded[i+1] = raw[i+2]
		decoded[i+2] = raw[i+1]
		decoded[i+3] = raw[i]
	}
	return net.IP(decoded).String(), nil
}

func (p *procPortSnapshotter) poll() ([]portlist.Port, error) {
	if p.poller == nil {
		return nil, nil
	}
	ports, changed, err := p.poller.Poll()
	if err != nil {
		return nil, err
	}
	if changed {
		p.cached = append(p.cached[:0], ports...)
	}
	return p.cached, nil
}

func (p *procPortSnapshotter) buildSnapshot(ports []portlist.Port, sockets []socketRow) []types.Port {
	type key struct {
		proto string
		port  uint16
	}

	addresses := make(map[key][]socketRow, len(sockets))
	for _, socket := range sockets {
		k := key{proto: socket.proto, port: socket.port}
		addresses[k] = append(addresses[k], socket)
	}

	snapshot := make([]types.Port, 0, len(ports))
	for _, port := range ports {
		k := key{proto: port.Proto, port: port.Port}
		snapshot = append(snapshot, types.Port{
			Addr:    selectSocketAddr(addresses[k]),
			Port:    port.Port,
			Proto:   port.Proto,
			PID:     port.Pid,
			Process: port.Process,
		})
	}
	return snapshot
}

func selectSocketAddr(sockets []socketRow) string {
	if len(sockets) == 0 {
		return ""
	}
	best := sockets[0]
	bestScore := socketAddrScore(best.addr)
	for _, socket := range sockets[1:] {
		score := socketAddrScore(socket.addr)
		if score < bestScore {
			best = socket
			bestScore = score
		}
	}
	return best.addr
}

func socketAddrScore(addr string) int {
	ip := net.ParseIP(addr)
	if ip == nil {
		return 4
	}
	if v4 := ip.To4(); v4 != nil {
		if v4.IsLoopback() {
			return 2
		}
		return 0
	}
	if ip.IsLoopback() {
		return 3
	}
	return 1
}
