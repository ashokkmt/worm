package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"worm/internal/model"
)

// SyslogTLSListener listens for Syslog streams over TLS (RFC 5425) using RFC 6587 framing.
type SyslogTLSListener struct {
	addr      string
	tlsConfig *tls.Config
	listener  net.Listener
	conns     map[net.Conn]struct{}
	mu        sync.Mutex
	closed    bool
}

// NewSyslogTLSListener creates an RFC 5425 TLS listener.
func NewSyslogTLSListener(addr string, tlsConfig *tls.Config) *SyslogTLSListener {
	if addr == "" {
		addr = ":6514"
	}
	return &SyslogTLSListener{
		addr:      addr,
		tlsConfig: tlsConfig,
		conns:     make(map[net.Conn]struct{}),
	}
}

func (l *SyslogTLSListener) Name() string {
	return "syslog-tls"
}

// Addr returns the resolved listening address.
func (l *SyslogTLSListener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listener != nil {
		return l.listener.Addr()
	}
	return nil
}

func (l *SyslogTLSListener) Start(ctx context.Context, out chan<- model.IngestedRecord) error {
	var ln net.Listener
	var err error

	if l.tlsConfig != nil {
		ln, err = tls.Listen("tcp", l.addr, l.tlsConfig)
	} else {
		ln, err = net.Listen("tcp", l.addr)
	}
	if err != nil {
		return fmt.Errorf("failed to listen Syslog TLS on %s: %w", l.addr, err)
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

			l.handleConn(ctx, c, out)
		}(conn)
	}

	wg.Wait()
	return nil
}

func (l *SyslogTLSListener) handleConn(ctx context.Context, conn net.Conn, out chan<- model.IngestedRecord) {
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
				trimmed := bytes.TrimRight(payload, "\r\n")
				if len(trimmed) > 0 {
					ackChan := make(chan error, 1)
					record := model.IngestedRecord{
						RawBytes:   trimmed,
						Transport:  "syslog_tls",
						SourceIP:   remoteHost,
						SourcePort: remotePort,
						ReceivedAt: time.Now().UTC(),
						Ack:        ackChan,
					}
					select {
					case out <- record:
						select {
						case <-ackChan:
						case <-ctx.Done():
							return
						}
					case <-ctx.Done():
						return
					}
				}
				continue
			}
		}

		// RFC 6587 non-transparent framing (newline-delimited)
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) > 0 {
				ackChan := make(chan error, 1)
				record := model.IngestedRecord{
					RawBytes:   trimmed,
					Transport:  "syslog_tls",
					SourceIP:   remoteHost,
					SourcePort: remotePort,
					ReceivedAt: time.Now().UTC(),
					Ack:        ackChan,
				}
				select {
				case out <- record:
					select {
					case <-ackChan:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}

		if err != nil {
			return
		}
	}
}

func (l *SyslogTLSListener) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.listener != nil {
		_ = l.listener.Close()
	}
	for conn := range l.conns {
		_ = conn.Close()
	}
	return nil
}
