// Command fakecollector provides the local OpAMP handshake required by the real
// Supervisor's bootstrap flow without running a telemetry Collector pipeline.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"gopkg.in/yaml.v3"
	"syscall"
)

const localConnectTimeout = 30 * time.Second

type collectorConfig struct {
	Extensions struct {
		OpAMP struct {
			Endpoint   string `yaml:"endpoint"`
			InstanceID string `yaml:"instance_uid"`
			Server     struct {
				WS struct {
					Endpoint string `yaml:"endpoint"`
				} `yaml:"ws"`
			} `yaml:"server"`
		} `yaml:"opamp"`
	} `yaml:"extensions"`
}

func main() {
	if err := run(); err != nil {
		message := fmt.Sprintf("%v; args=%q", err, os.Args)
		fmt.Fprintln(os.Stderr, message)
		if path := os.Getenv("FLEET_SUPERVISOR_TEST_CHILD_LOG_FILE"); path != "" {
			file, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if openErr == nil {
				_, _ = fmt.Fprintln(file, message)
				_ = file.Close()
			}
		}
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "featuregate" {
		return nil
	}
	configPath := flag.String("config", "", "effective Collector configuration")
	flag.Parse()
	if *configPath == "" {
		return errors.New("supervisor did not provide a Collector config path")
	}
	configBytes, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("read effective Collector config: %w", err)
	}
	var cfg collectorConfig
	if err := yaml.Unmarshal(configBytes, &cfg); err != nil {
		return fmt.Errorf("parse effective Collector config: %w", err)
	}
	endpoint := cfg.Extensions.OpAMP.Endpoint
	if endpoint == "" {
		endpoint = cfg.Extensions.OpAMP.Server.WS.Endpoint
	}
	if endpoint == "" {
		return errors.New("effective Collector config has no Supervisor OpAMP endpoint")
	}
	instanceID, err := uuid.Parse(cfg.Extensions.OpAMP.InstanceID)
	if err != nil {
		return fmt.Errorf("parse Supervisor injected instance UID: %w", err)
	}

	if path := os.Getenv("FLEET_SUPERVISOR_TEST_CHILD_PID_FILE"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open child PID file: %w", err)
		}
		_, writeErr := fmt.Fprintln(file, os.Getpid())
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write child PID file: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close child PID file: %w", closeErr)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opampClient, err := newOpAMPClient(endpoint)
	if err != nil {
		return err
	}
	if err := opampClient.SetAgentDescription(&protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{{
			Key:   "service.name",
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "test.fakecollector"}},
		}},
	}); err != nil {
		return fmt.Errorf("set test Collector description: %w", err)
	}
	var instanceUID types.InstanceUid
	copy(instanceUID[:], instanceID[:])
	connected := make(chan struct{})
	var connectOnce sync.Once
	if err := opampClient.Start(ctx, types.StartSettings{
		OpAMPServerURL: endpoint,
		InstanceUid:    instanceUID,
		Callbacks: types.Callbacks{OnConnect: func(context.Context) {
			connectOnce.Do(func() { close(connected) })
		}},
	}); err != nil {
		return fmt.Errorf("start local Supervisor OpAMP client: %w", err)
	}

	select {
	case <-connected:
	case <-time.After(localConnectTimeout):
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = opampClient.Stop(stopCtx)
		return errors.New("collector could not connect to local Supervisor OpAMP endpoint")
	case <-ctx.Done():
		return stopClient(opampClient)
	}

	<-ctx.Done()
	return stopClient(opampClient)
}

func newOpAMPClient(endpoint string) (client.OpAMPClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse local Supervisor OpAMP endpoint: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ws", "wss":
		return client.NewWebSocket(nil), nil
	case "http", "https":
		return client.NewHTTP(nil), nil
	default:
		return nil, fmt.Errorf("unsupported local Supervisor OpAMP endpoint scheme %q", parsed.Scheme)
	}
}

func stopClient(opampClient client.OpAMPClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := opampClient.Stop(ctx); err != nil {
		return fmt.Errorf("stop local Supervisor OpAMP client: %w", err)
	}
	return nil
}
