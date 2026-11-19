package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// testOptions supplies bounded timeouts and an ephemeral loopback listener.
func testOptions() Options {
	return Options{
		ListenAddress:     "127.0.0.1:0",
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
	}
}

// TestServerServesAndShutsDown verifies successful listener lifecycle.
func TestServerServesAndShutsDown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server, err := New(testOptions(), mux)
	if err != nil {
		t.Fatalf("construct server: %v", err)
	}

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown server: %v", err)
		}
	})

	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()

	response, err := client.Get("http://" + server.Addr().String() + "/test")
	if err != nil {
		t.Fatalf("send request: %v", err)
	}

	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if err, ok := <-server.Errors(); ok {
		t.Fatalf("unexpected serving error: %v", err)
	}
}

// TestServerReturnsBindFailure ensures an occupied port fails during startup.
func TestServerReturnsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}

	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	})

	cfg := testOptions()
	cfg.ListenAddress = listener.Addr().String()

	server, err := New(cfg, http.NewServeMux())
	if err != nil {
		t.Fatalf("construct server: %v", err)
	}

	if err := server.Start(context.Background()); err == nil {
		t.Fatal("expected occupied port to fail")
	}
}

// TestServerReportsUnexpectedListenerFailure verifies runtime error forwarding.
func TestServerReportsUnexpectedListenerFailure(t *testing.T) {
	server, err := New(testOptions(), http.NewServeMux())
	if err != nil {
		t.Fatalf("construct server: %v", err)
	}

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}

	if err := server.listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	select {
	case err := <-server.Errors():
		if !errors.Is(err, ErrServe) {
			t.Fatalf("expected serving failure, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serving failure was not reported")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestServerRejectsPublicPlaintext prevents accidental network exposure.
func TestServerRejectsPublicPlaintext(t *testing.T) {
	cfg := testOptions()
	cfg.ListenAddress = "0.0.0.0:8081"

	if _, err := New(cfg, http.NewServeMux()); err == nil {
		t.Fatal("expected public plaintext listener to be rejected")
	}
}
