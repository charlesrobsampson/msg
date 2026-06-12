// mock-server runs in-memory mocks of the OpenMessage and Signal CLI REST APIs
// on fixed ports for manual exploration without any real external services.
//
// Usage:
//
//	go run ./tests/mock-server
//	  — or —
//	task mock
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/charlesrobsampson/msg/tests/mocks"
)

const (
	omPort     = 17007
	signalPort = 18181
)

func main() {
	mockOM := mocks.NewOpenMessageMockOnPort(omPort)
	mockSignal := mocks.NewSignalMockOnPort(signalPort)

	mocks.SeedDefault(mockOM, mockSignal)

	fmt.Println("Mock servers running:")
	fmt.Printf("  OpenMessage API : %s\n", mockOM.URL())
	fmt.Printf("  Signal CLI API  : %s\n", mockSignal.URL())
	fmt.Println()
	fmt.Println("Run msg against them:")
	fmt.Printf("  OPENMESSAGES_PORT=%d SIGNAL_PORT=%d SIGNAL_ACCOUNT=%s MSG_SKIP_STARTUP=true ./msg\n",
		omPort, signalPort, mocks.TestSignalAccount)
	fmt.Printf("  OPENMESSAGES_PORT=%d SIGNAL_PORT=%d SIGNAL_ACCOUNT=%s MSG_SKIP_STARTUP=true ./msg list\n",
		omPort, signalPort, mocks.TestSignalAccount)
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop.")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	mockOM.Close()
	mockSignal.Close()
	fmt.Println("\nMock servers stopped.")
}
