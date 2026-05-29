package server

import (
	"fmt"
	"net"
	"os"
)

func testWriteFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o600)
}

type ephAddr struct {
	host string
	port int
}

func getEphemeralAddr() (*ephAddr, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().(*net.TCPAddr)
	if err := l.Close(); err != nil {
		return nil, err
	}
	if addr.Port == 0 {
		return nil, fmt.Errorf("no port allocated")
	}
	return &ephAddr{host: "127.0.0.1", port: addr.Port}, nil
}
