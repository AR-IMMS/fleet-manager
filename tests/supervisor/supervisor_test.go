//go:build supervisor

package supervisor_test

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/delivery/opamp"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	platformconfig "github.com/AR-IMMS/fleet-manager/internal/platform/config"
	"gopkg.in/yaml.v3"
)

const (
	testCredential       = "supervisor-integration-token-012345678901234567890"
	testAgentID          = "supervisor-uid-continuity-agent"
	testCredentialID     = "supervisor-uid-continuity-credential"
	messageLimit         = 1 << 20
	supervisorStartLimit = 30 * time.Second
)

type uidEvent struct {
	uid []byte
	err error
}

type acceptingVerifier struct{}

func (acceptingVerifier) Verify(_ context.Context, credential string) (domain.Principal, error) {
	if credential != testCredential {
		return domain.Principal{}, domain.ErrCredentialRejected
	}
	return domain.NewPrincipal(testAgentID, testCredentialID)
}

type acceptingAuthorizer struct{ events chan<- uidEvent }

func (a acceptingAuthorizer) BindAndAuthorize(_ context.Context, _ domain.Principal, uid []byte) error {
	select {
	case a.events <- uidEvent{uid: append([]byte(nil), uid...)}:
	default:
	}
	return nil
}

type opampHarness struct {
	t          *testing.T
	root       string
	collector  string
	pidFile    string
	childLog   string
	processes  []*supervisorProcess
	logBuffers []*safeBuffer
}

func newHarness(t *testing.T) *opampHarness {
	t.Helper()
	bin := os.Getenv("OPAMP_SUPERVISOR_BIN")
	collector := os.Getenv("FLEET_TEST_COLLECTOR_BIN")
	if bin == "" || collector == "" {
		requireIntegrationDependency(t, "OPAMP_SUPERVISOR_BIN and FLEET_TEST_COLLECTOR_BIN are required")
	}
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatalf("read Supervisor build information: %v", err)
	}
	if info.Path != "github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor" ||
		(info.Main.Version != "v0.160.0" && info.Main.Version != "v0.160.0+dirty") {
		t.Fatalf("expected pinned Supervisor module v0.160.0, got path=%q version=%q", info.Path, info.Main.Version)
	}
	h := &opampHarness{
		t:         t,
		root:      t.TempDir(),
		collector: collector,
		pidFile:   filepath.Join(t.TempDir(), "collector-pids.txt"),
		childLog:  filepath.Join(t.TempDir(), "collector.log"),
	}
	t.Cleanup(func() {
		h.stopAll()
		if t.Failed() {
			h.writeRedactedLogs()
		}
	})
	return h
}

func requireIntegrationDependency(t *testing.T, message string) {
	t.Helper()
	if os.Getenv("FLEET_REQUIRE_INTEGRATION") == "true" {
		t.Fatal(message)
	}
	t.Skip(message)
}

func (h *opampHarness) server(t *testing.T, authorizer application.SessionAuthorizer) (*opamp.OpAMPServer, <-chan uidEvent) {
	t.Helper()
	events := make(chan uidEvent, 1024)
	if authorizer == nil {
		authorizer = acceptingAuthorizer{events: events}
	} else {
		authorizer = eventAuthorizer{delegate: authorizer, events: events}
	}
	authenticator, err := application.NewAuthenticator(acceptingVerifier{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	callbacks, err := opamp.NewCallbackAdapter(authenticator, authorizer, logger)
	if err != nil {
		t.Fatal(err)
	}
	server, err := opamp.NewServer(platformconfig.OpAMPConfig{
		ListenAddress:   "127.0.0.1:0",
		OpAMPPath:       "/v1/opamp",
		MaxMessageBytes: messageLimit,
		AuthTimeout:     5 * time.Second,
	}, callbacks, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal("start Fleet test OpAMP server", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Stop(ctx); err != nil {
			t.Errorf("stop Fleet test OpAMP server: %v", err)
		}
	})
	return server, events
}

type eventAuthorizer struct {
	delegate application.SessionAuthorizer
	events   chan<- uidEvent
}

func (a eventAuthorizer) BindAndAuthorize(ctx context.Context, principal domain.Principal, uid []byte) error {
	err := a.delegate.BindAndAuthorize(ctx, principal, uid)
	select {
	case a.events <- uidEvent{uid: append([]byte(nil), uid...), err: err}:
	default:
	}
	return err
}

type supervisorConfig struct {
	Server struct {
		Endpoint string            `yaml:"endpoint"`
		Headers  map[string]string `yaml:"headers"`
	} `yaml:"server"`
	Agent struct {
		Executable      string            `yaml:"executable"`
		Env             map[string]string `yaml:"env"`
		ConfigFiles     []string          `yaml:"config_files,omitempty"`
		OpAMPServerPort int               `yaml:"opamp_server_port"`
	} `yaml:"agent"`
	Storage struct {
		Directory string `yaml:"directory"`
	} `yaml:"storage"`
	Telemetry struct {
		Logs struct {
			Encoding string `yaml:"encoding"`
			Level    string `yaml:"level"`
		} `yaml:"logs"`
	} `yaml:"telemetry"`
}

func (h *opampHarness) start(t *testing.T, endpoint, stateDir string) *supervisorProcess {
	return h.startWithCredential(t, endpoint, stateDir, testCredential)
}

func (h *opampHarness) startWithCredential(t *testing.T, endpoint, stateDir, credential string) *supervisorProcess {
	t.Helper()
	if credential == "" {
		t.Fatal("Supervisor test credential is required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal("create Supervisor state directory", err)
	}
	cfg := supervisorConfig{}
	cfg.Server.Endpoint = endpoint
	cfg.Server.Headers = map[string]string{"Authorization": "Bearer " + credential}
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve local Supervisor OpAMP port", err)
	}
	cfg.Agent.OpAMPServerPort = portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatal("release local Supervisor OpAMP port", err)
	}
	cfg.Agent.Executable = h.collector
	cfg.Agent.Env = map[string]string{
		"FLEET_SUPERVISOR_TEST_CHILD_PID_FILE": h.pidFile,
		"FLEET_SUPERVISOR_TEST_CHILD_LOG_FILE": h.childLog,
	}
	cfg.Storage.Directory = stateDir
	cfg.Telemetry.Logs.Encoding = "console"
	cfg.Telemetry.Logs.Level = "info"
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal("marshal Supervisor test config", err)
	}
	configPath := filepath.Join(h.root, fmt.Sprintf("supervisor-%d.yaml", len(h.processes)+1))
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal("write Supervisor test config", err)
	}
	cmd := exec.Command(os.Getenv("OPAMP_SUPERVISOR_BIN"), "--config="+configPath)
	logs := &safeBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal("start official OpAMP Supervisor", err)
	}
	process := &supervisorProcess{cmd: cmd, done: make(chan error, 1), logs: logs}
	go func() { process.done <- cmd.Wait() }()
	h.processes = append(h.processes, process)
	h.logBuffers = append(h.logBuffers, logs)
	return process
}

func (h *opampHarness) endpoint(server *opamp.OpAMPServer) string {
	return (&url.URL{Scheme: "ws", Host: server.Addr().String(), Path: "/v1/opamp"}).String()
}

func (h *opampHarness) waitUID(events <-chan uidEvent) uidEvent {
	h.t.Helper()
	timer := time.NewTimer(supervisorStartLimit)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if len(event.uid) != 16 {
				h.t.Fatalf("Supervisor sent UID with %d bytes; expected 16", len(event.uid))
			}
			return event
		case <-timer.C:
			h.t.Fatal("timed out waiting for authenticated Supervisor OpAMP message")
		case <-h.t.Context().Done():
			h.t.Fatal("test context ended while waiting for Supervisor")
		}
	}
}

func drainUIDEvents(events <-chan uidEvent) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func (h *opampHarness) stopAll() {
	for _, process := range h.processes {
		process.stop()
	}
	h.stopCollectorChildren()
}

func (h *opampHarness) stopCollectorChildren() {
	data, err := os.ReadFile(h.pidFile)
	if err != nil {
		return
	}
	defer func() {
		if err := os.WriteFile(h.pidFile, nil, 0o600); err != nil && !os.IsNotExist(err) {
			h.t.Errorf("clear test Collector PID file: %v", err)
		}
	}()
	for _, line := range strings.Fields(string(data)) {
		var pid int
		if _, err := fmt.Sscan(line, &pid); err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Kill()
		}
	}
}

func (h *opampHarness) writeRedactedLogs() {
	logDir := os.Getenv("SUPERVISOR_TEST_LOG_DIR")
	var output strings.Builder
	for i, logs := range h.logBuffers {
		fmt.Fprintf(&output, "===== Supervisor run %d =====\n%s\n", i+1, redact(logs.String()))
	}
	if childLogs, err := os.ReadFile(h.childLog); err == nil {
		fmt.Fprintf(&output, "===== Test Collector logs =====\n%s\n", redact(string(childLogs)))
	}
	if logDir == "" {
		h.t.Logf("redacted Supervisor logs:\n%s", output.String())
		return
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		h.t.Logf("could not create redacted log directory: %v", err)
		return
	}
	path := filepath.Join(logDir, "supervisor-test.log")
	if err := os.WriteFile(path, []byte(output.String()), 0o600); err != nil {
		h.t.Logf("could not write redacted Supervisor logs: %v", err)
	}
}

var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)

func redact(value string) string { return bearerPattern.ReplaceAllString(value, `${1}[REDACTED]`) }

type supervisorProcess struct {
	cmd  *exec.Cmd
	done chan error
	logs *safeBuffer
	once sync.Once
}

func (p *supervisorProcess) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.once.Do(func() {
		if runtime.GOOS != "windows" {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		select {
		case err := <-p.done:
			p.recordStopError(err)
			return
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			p.recordStopError(<-p.done)
		}
	})
}

func (p *supervisorProcess) recordStopError(err error) {
	if err != nil {
		_, _ = fmt.Fprintf(p.logs, "Supervisor process exit during cleanup: %v\n", err)
	}
}

type safeBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *safeBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestSupervisorUIDContinuityWithRetainedAndFreshState(t *testing.T) {
	h := newHarness(t)
	server, events := h.server(t, nil)
	retainedDir := filepath.Join(h.root, "retained-state")
	firstProcess := h.start(t, h.endpoint(server), retainedDir)
	first := h.waitUID(events)
	if first.err != nil {
		t.Fatalf("first Supervisor UID was rejected: %v", first.err)
	}
	firstProcess.stop()

	secondProcess := h.start(t, h.endpoint(server), retainedDir)
	second := h.waitUID(events)
	if second.err != nil {
		t.Fatalf("same-state Supervisor reconnect was rejected: %v", second.err)
	}
	if !bytes.Equal(first.uid, second.uid) {
		t.Fatalf("UID changed with retained state: first=%x second=%x", first.uid, second.uid)
	}
	secondProcess.stop()
	drainUIDEvents(events)

	thirdProcess := h.start(t, h.endpoint(server), filepath.Join(h.root, "fresh-state"))
	third := h.waitUID(events)
	if third.err != nil {
		t.Fatalf("fresh-state UID was rejected by continuity observer: %v", third.err)
	}
	if bytes.Equal(first.uid, third.uid) {
		t.Fatalf("fresh Supervisor state reused UID %x; expected a new UID", first.uid)
	}
	thirdProcess.stop()
	t.Logf("Supervisor v0.160.0 UID continuity: retained=%x fresh=%x", first.uid, third.uid)
}
