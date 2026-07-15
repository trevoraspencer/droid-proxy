package migration

import (
	"fmt"
	"net"
	"strconv"
)

// DestinationReservation holds a bound TCP listener on the migration
// destination port. It ensures the destination remains available through
// the coherent restart transition, not just a transient bind-and-release
// check. Production managed restart callers must use this rather than
// passing a nil DestinationChecker.
type DestinationReservation struct {
	listener net.Listener
	host     string
	port     int
}

// ReserveDestination attempts to bind a TCP listener on host:port. If
// successful, it returns a DestinationReservation whose HeldChecker can be
// passed as ManagedRestartOptions.DestinationChecker. The caller must call
// Close after the restart transition completes (or on failure) to release
// the port for the new process to bind.
func ReserveDestination(host string, port int) (*DestinationReservation, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("destination %s is not available: %w", addr, err)
	}
	return &DestinationReservation{listener: l, host: host, port: port}, nil
}

// HeldChecker returns a DestinationChecker function suitable for
// ManagedRestartOptions.DestinationChecker. It verifies the reservation is
// still held (the listener has not been closed or stolen) and that the
// requested host and port exactly match the reservation it owns. A nil
// checker is never returned by production callers.
func (r *DestinationReservation) HeldChecker() func(string, int) error {
	return func(host string, port int) error {
		if r == nil || r.listener == nil {
			return fmt.Errorf("destination reservation was not held")
		}
		if host != r.host || port != r.port {
			return fmt.Errorf("destination check host/port %s:%d does not match reserved %s:%d",
				host, port, r.host, r.port)
		}
		return nil
	}
}

// Close releases the destination reservation so the restarted process can
// bind the port. It must be called after the coherent restart transition.
func (r *DestinationReservation) Close() error {
	if r == nil || r.listener == nil {
		return nil
	}
	err := r.listener.Close()
	r.listener = nil
	return err
}
