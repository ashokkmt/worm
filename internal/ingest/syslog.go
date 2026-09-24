package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"worm/internal/model"
)

// SyslogUDPListener listens for RFC 5424 / RFC 3164 Syslog datagrams over UDP.
type SyslogUDPListener struct {
	addr   string
	conn   *net.UDPConn
	mu     sync.Mutex
	closed bool
}

// NewSyslogUDPListener creates a UDP listener on the specified address (e.g. ":514" or ":1514").
func NewSyslogUDPListener(addr string) *SyslogUDPListener {
	if addr == "" {
		addr = ":514"
	}
	return &SyslogUDPListener{addr: addr}
}

func (l *SyslogUDPListener) Name() string {
	return "syslog-udp"
}

// Addr returns the resolved listening address (useful when bound to port 0 for testing).
func (l *SyslogUDPListener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		return l.conn.LocalAddr()
	}
	return nil
}

func (l *SyslogUDPListener) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	laddr, err := net.ResolveUDPAddr("udp", l.addr)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address %s: %w", l.addr, err)
	}

	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return fmt.Errorf("failed to listen UDP on %s: %w", l.addr, err)
	}

	l.mu.Lock()
	l.conn = conn
	l.closed = false
	l.mu.Unlock()

	defer func() {
		_ = conn.Close()
	}()

	// Buffer for datagrams (standard max UDP payload)
	buf := make([]byte, 65535)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed {
				return nil
			}
			continue
		}

		if n == 0 {
			continue
		}

		payload := make([]byte, n)
		copy(payload, buf[:n])

		rec := model.IngestedRecord{
			Transport:  "syslog-udp",
			SourceIP:   remoteAddr.IP.String(),
			SourcePort: remoteAddr.Port,
			RawBytes:   payload,
			ReceivedAt: time.Now().UTC(),
		}

		select {
		case out <- rec:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *SyslogUDPListener) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.conn != nil {
		return l.conn.Close()
	}
	return nil
}

// SyslogTCPListener listens for Syslog streams over TCP using newline or octet-counting framing.
type SyslogTCPListener struct {
	addr     string
	listener net.Listener
	conns    map[net.Conn]struct{}
	mu       sync.Mutex
	closed   bool
}

// NewSyslogTCPListener creates a TCP listener on the specified address.
func NewSyslogTCPListener(addr string) *SyslogTCPListener {
	if addr == "" {
		addr = ":514"
	}
	return &SyslogTCPListener{
		addr:  addr,
		conns: make(map[net.Conn]struct{}),
	}
}

func (l *SyslogTCPListener) Name() string {
	return "syslog-tcp"
}

// Addr returns the resolved listening address.
func (l *SyslogTCPListener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listener != nil {
		return l.listener.Addr()
	}
	return nil
}

func (l *SyslogTCPListener) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	ln, err := net.Listen("tcp", l.addr)
	if err != nil {
		return fmt.Errorf("failed to listen TCP on %s: %w", l.addr, err)
	}

	l.mu.Lock()
	l.listener = ln
	l.closed = false
	l.mu.Unlock()

	defer func() {
		_ = ln.Close()
	}()

	var wg sync.WaitGroup

	go func() {
		<-ctx.Done()
		_ = l.Stop()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed {
				break
			}
			continue
		}

		l.mu.Lock()
		l.conns[conn] = struct{}{}
		l.mu.Unlock()

		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer func() {
				_ = c.Close()
				l.mu.Lock()
				delete(l.conns, c)
				l.mu.Unlock()
			}()

			l.handleTCPConn(ctx, c, out)
		}(conn)
	}

	wg.Wait()
	return nil
}

func (l *SyslogTCPListener) handleTCPConn(ctx context.Context, conn net.Conn, out chan<- model.IngestedRecord) {
	defer conn.Close()
	remoteHost, remotePortStr, _ := net.SplitHostPort(conn.RemoteAddr().String())
	remotePort, _ := strconv.Atoi(remotePortStr)

	reader := bufio.NewReader(conn)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Peek first byte to determine framing (RFC 6587)
		peekBytes, err := reader.Peek(1)
		if err != nil {
			return
		}

		if peekBytes[0] >= '1' && peekBytes[0] <= '9' {
			// RFC 6587 octet counting: <MSG-LEN> <MSG>
			lenStr, err := reader.ReadString(' ')
			if err != nil {
				return
			}
			msgLen, err := strconv.Atoi(strings.TrimSpace(lenStr))
			if err == nil && msgLen > 0 && msgLen <= 10*1024*1024 {
				payload := make([]byte, msgLen)
				_, err := io.ReadFull(reader, payload)
				if err != nil {
					return
				}
				rec := model.IngestedRecord{
					Transport:  "syslog-tcp",
					SourceIP:   remoteHost,
					SourcePort: remotePort,
					RawBytes:   payload,
					ReceivedAt: time.Now().UTC(),
				}
				select {
				case out <- rec:
				case <-ctx.Done():
					return
				}
				continue
			}
		}

		// Non-transparent framing: newline delimited
		lineBytes, err := reader.ReadBytes('\n')
		if err != nil && len(lineBytes) == 0 {
			return
		}
		payload := bytes.TrimRight(lineBytes, "\r\n")
		if len(payload) == 0 {
			if err != nil {
				return
			}
			continue
		}

		rec := model.IngestedRecord{
			Transport:  "syslog-tcp",
			SourceIP:   remoteHost,
			SourcePort: remotePort,
			RawBytes:   payload,
			ReceivedAt: time.Now().UTC(),
		}

		select {
		case out <- rec:
		case <-ctx.Done():
			return
		}

		if err != nil {
			return
		}
	}
}

func (l *SyslogTCPListener) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true

	var err error
	if l.listener != nil {
		err = l.listener.Close()
	}

	for c := range l.conns {
		_ = c.Close()
	}
	l.conns = make(map[net.Conn]struct{})

	return err
}
