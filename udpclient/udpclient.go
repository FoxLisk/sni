package udpclient

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type UDPClient struct {
	name string

	c    *net.UDPConn
	addr *net.UDPAddr

	muteLog bool

	isConnected bool
	isClosed    bool

	seqLock sync.Mutex
}

func NewUDPClient(name string) *UDPClient {
	return &UDPClient{
		name: name,
	}
}

func MakeUDPClient(name string, c *UDPClient) *UDPClient {
	c.name = name
	return c
}

func (c *UDPClient) Name() string { return c.name }

func (c *UDPClient) IsClosed() bool { return c.isClosed }

func (c *UDPClient) MuteLog(muted bool) {
	c.muteLog = muted
}

func (c *UDPClient) Address() *net.UDPAddr { return c.addr }

var ErrTimeout = fmt.Errorf("timeout")

func (c *UDPClient) WriteWithDeadline(m []byte, deadline time.Time) (err error) {
	if c.isClosed {
		return net.ErrClosed
	}

	err = c.c.SetWriteDeadline(deadline)
	if err != nil {
		return
	}

	_, err = c.c.Write(m)
	if err != nil {
		if isTimeoutError(err) {
			_ = c.Close()
		}
		if errors.Is(err, net.ErrClosed) {
			_ = c.Close()
		}
		return
	}
	return
}

func (c *UDPClient) ReadWithDeadline(deadline time.Time) (b []byte, err error) {
	if c.isClosed {
		return nil, net.ErrClosed
	}

	// wait for a packet from UDP socket:
	err = c.c.SetReadDeadline(deadline)
	if err != nil {
		return
	}

	var n int
	b = make([]byte, 65536)
	n, _, err = c.c.ReadFromUDP(b)
	if err != nil {
		b = nil
		if isTimeoutError(err) {
			_ = c.Close()
		}
		if errors.Is(err, net.ErrClosed) {
			_ = c.Close()
		}
		return
	}

	b = b[:n]
	return
}

func (c *UDPClient) ReadWithDeadlineInto(deadline time.Time, b []byte) (n int, err error) {
	if c.isClosed {
		return 0, net.ErrClosed
	}

	// wait for a packet from UDP socket:
	err = c.c.SetReadDeadline(deadline)
	if err != nil {
		return
	}

	n, _, err = c.c.ReadFromUDP(b)
	if err != nil {
		b = nil
		if isTimeoutError(err) {
			_ = c.Close()
		}
		if errors.Is(err, net.ErrClosed) {
			_ = c.Close()
		}
		return
	}

	return
}

func (c *UDPClient) WriteThenRead(m []byte, deadline time.Time) (rsp []byte, err error) {
	if c.isClosed {
		return nil, net.ErrClosed
	}

	// hold a lock so we're guaranteed write->read consistency:
	defer c.seqLock.Unlock()
	c.seqLock.Lock()

	err = c.WriteWithDeadline(m, deadline)
	if err != nil {
		return
	}
	rsp, err = c.ReadWithDeadline(deadline)
	if err != nil {
		return
	}
	return
}

func (c *UDPClient) Lock() {
	//fmt.Printf("%s lock\n", c.name)
	c.seqLock.Lock()
}
func (c *UDPClient) Unlock() {
	//fmt.Printf("%s unlock\n", c.name)
	c.seqLock.Unlock()
}

func (c *UDPClient) SetReadDeadline(t time.Time) error  { return c.c.SetReadDeadline(t) }
func (c *UDPClient) SetWriteDeadline(t time.Time) error { return c.c.SetWriteDeadline(t) }

func (c *UDPClient) IsConnected() bool { return c.isConnected }

func (c *UDPClient) Log(format string, args ...interface{}) {
	if c.muteLog {
		return
	}
	s := fmt.Sprintf(format, args...)
	log.Printf("%s: %s", c.name, s)
}

func (c *UDPClient) Connect(addr *net.UDPAddr) (err error) {
	c.Log("connect to server '%s'\n", addr)

	if c.isConnected {
		return fmt.Errorf("%s: already connected", c.name)
	}

	c.addr = addr

	c.c, err = net.DialUDP("udp", nil, addr)
	if err != nil {
		return
	}

	c.isConnected = true
	c.Log("connected to server '%s'\n", addr)

	return
}

func (c *UDPClient) Disconnect() {
	c.Log("disconnect from server '%s'\n", c.addr)

	if !c.isConnected {
		return
	}

	// close the underlying connection:
	err := c.Close()
	if err != nil {
		c.Log("close: %v\n", err)
	}

	c.Log("disconnected from server '%s'\n", c.addr)
}

func (c *UDPClient) Close() (err error) {
	if !c.isConnected {
		return
	}

	if c.c != nil {
		err = c.c.Close()
	}

	c.isClosed = true
	c.isConnected = false
	c.c = nil
	return
}

func isTimeoutError(err error) bool {
	e, ok := err.(net.Error)
	return ok && e.Timeout()
}
