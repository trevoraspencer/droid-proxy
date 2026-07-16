package migration

import (
	"net"
	"strconv"
	"testing"
)

// TestReserveDestinationSucceeds verifies that reserving an available port
// creates a valid reservation with a working held checker.
func TestReserveDestinationSucceeds(t *testing.T) {
	// Get an OS-assigned port by binding temporarily.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()

	reservation, err := ReserveDestination("127.0.0.1", addr.Port)
	if err != nil {
		t.Fatalf("ReserveDestination: %v", err)
	}
	defer reservation.Close()

	checker := reservation.HeldChecker()
	if err := checker("127.0.0.1", addr.Port); err != nil {
		t.Fatalf("held checker failed: %v", err)
	}
}

// TestReserveDestinationOccupiedRefuses verifies that reserving an occupied
// port fails, which is the destination protection mechanism.
func TestReserveDestinationOccupiedRefuses(t *testing.T) {
	// Bind a listener to occupy the port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	addr := l.Addr().(*net.TCPAddr)

	_, err = ReserveDestination("127.0.0.1", addr.Port)
	if err == nil {
		t.Fatal("expected error for occupied destination")
	}
}

// TestHeldCheckerAfterCloseFails verifies that after the reservation is
// closed, the held checker reports failure (protection no longer held).
func TestHeldCheckerAfterCloseFails(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()

	reservation, err := ReserveDestination("127.0.0.1", addr.Port)
	if err != nil {
		t.Fatalf("ReserveDestination: %v", err)
	}

	checker := reservation.HeldChecker()

	// Close the reservation.
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	// Held checker should now fail (listener closed).
	if err := checker("127.0.0.1", addr.Port); err == nil {
		t.Fatal("expected held checker to fail after close")
	}
}

// TestReserveDestinationHoldsPort verifies that while the reservation is
// held, another listener cannot bind the same port.
func TestReserveDestinationHoldsPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()

	reservation, err := ReserveDestination("127.0.0.1", addr.Port)
	if err != nil {
		t.Fatalf("ReserveDestination: %v", err)
	}
	defer reservation.Close()

	// Try to bind the same port — should fail because reservation holds it.
	_, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(addr.Port))
	if err == nil {
		t.Fatal("expected second bind to fail while reservation is held")
	}
}

// TestNilReservationHeldChecker verifies that a nil reservation's held
// checker fails, proving production callers must not pass nil.
func TestNilReservationHeldChecker(t *testing.T) {
	var r *DestinationReservation
	checker := r.HeldChecker()
	if err := checker("127.0.0.1", 9787); err == nil {
		t.Fatal("nil reservation's held checker should fail")
	}
}

// TestHeldCheckerRejectsWrongHost verifies that HeldChecker rejects any host
// different from the reserved host, proving the reservation is bound to its
// exact owned address.
func TestHeldCheckerRejectsWrongHost(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()

	reservation, err := ReserveDestination("127.0.0.1", addr.Port)
	if err != nil {
		t.Fatalf("ReserveDestination: %v", err)
	}
	defer reservation.Close()

	checker := reservation.HeldChecker()
	// Correct host and port must pass.
	if err := checker("127.0.0.1", addr.Port); err != nil {
		t.Fatalf("checker rejected matching host/port: %v", err)
	}
	// Wrong host must fail.
	if err := checker("localhost", addr.Port); err == nil {
		t.Fatal("checker must reject a host different from the reserved host")
	}
}

// TestHeldCheckerRejectsWrongPort verifies that HeldChecker rejects any port
// different from the reserved port.
func TestHeldCheckerRejectsWrongPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()

	reservation, err := ReserveDestination("127.0.0.1", addr.Port)
	if err != nil {
		t.Fatalf("ReserveDestination: %v", err)
	}
	defer reservation.Close()

	checker := reservation.HeldChecker()
	// Wrong port must fail even with the correct host.
	if err := checker("127.0.0.1", addr.Port+1); err == nil {
		t.Fatal("checker must reject a port different from the reserved port")
	}
}
