package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type sessionRecord struct {
	PID        int    `json:"pid"`
	Port       int    `json:"port"`
	Executable string `json:"executable"`
	ConfigPath string `json:"config_path,omitempty"`
}

func openListener(port int) (net.Listener, int, func(), error) {
	return openListenerOnHost("127.0.0.1", port)
}

func openListenerOnHost(host string, port int) (net.Listener, int, func(), error) {
	return openListenerOnHostWithConfig(host, port, "")
}

func openListenerOnHostWithConfig(host string, port int, configPath string) (net.Listener, int, func(), error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		port = 9090
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err == nil {
		return registerSession(listener, configPath)
	}
	if !isAddressInUse(err) {
		return nil, 0, nil, err
	}
	for _, pid := range ownerPIDs(port) {
		if pid == os.Getpid() || !processIsGhrouter(pid) {
			continue
		}
		if err := stopProcess(pid); err != nil {
			continue
		}
		listener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
		if err == nil {
			return registerSession(listener, configPath)
		}
	}
	listener, err = net.Listen("tcp", fmt.Sprintf("%s:0", host))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("listen on configured port %d and fallback port: %w", port, err)
	}
	return registerSession(listener, configPath)
}

func validateBindHost(host string, aclEnabled bool) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if !aclEnabled {
		return fmt.Errorf("non-loopback bind host %q requires ACL authentication", host)
	}
	return nil
}

func registerSession(listener net.Listener, configPath string) (net.Listener, int, func(), error) {
	port := listener.Addr().(*net.TCPAddr).Port
	record := sessionRecord{PID: os.Getpid(), Port: port, Executable: executablePath(), ConfigPath: absolutePath(configPath)}
	path := sessionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		if data, marshalErr := json.Marshal(record); marshalErr == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	}
	cleanup := func() {
		current, ok := readSession(port)
		if ok && current.PID == os.Getpid() {
			_ = os.Remove(path)
		}
	}
	return listener, port, cleanup, nil
}

func readSession(port int) (sessionRecord, bool) {
	record, ok := readSessionRecord()
	if !ok || record.Port != port {
		return sessionRecord{}, false
	}
	return record, true
}

func readSessionRecord() (sessionRecord, bool) {
	data, err := os.ReadFile(sessionPath())
	if err != nil {
		return sessionRecord{}, false
	}
	var record sessionRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Port <= 0 || record.PID <= 0 {
		return sessionRecord{}, false
	}
	return record, true
}

// ActiveSessionPort returns the live listener port for the requested config.
// It deliberately ignores stale or cross-config session records.
func ActiveSessionPort(configured int, configPath string) (int, bool) {
	record, ok := readSessionRecord()
	if !ok || absolutePath(configPath) != record.ConfigPath {
		return configured, false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", record.Port), 100*time.Millisecond)
	if err != nil {
		return configured, false
	}
	_ = conn.Close()
	return record.Port, true
}

func absolutePath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

func sessionPath() string {
	if root := strings.TrimSpace(os.Getenv("GHR_RUNTIME_DIR")); root != "" {
		return filepath.Join(root, "session.json")
	}
	if root, err := os.UserCacheDir(); err == nil && root != "" {
		return filepath.Join(root, "ghrouter", "session.json")
	}
	return filepath.Join(os.TempDir(), "ghrouter-session.json")
}

func executablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "ghrouter"
	}
	return path
}

func processIsGhrouter(pid int) bool {
	if pid <= 0 {
		return false
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(string(output)))
	return strings.Contains(filepath.Base(name), "ghrouter")
}

func ownerPIDs(port int) []int {
	output, err := exec.Command("lsof", "-nP", "-t", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	pids := make([]int, 0, len(lines))
	for _, line := range lines {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func stopProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(os.Interrupt); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(process) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return process.Kill()
}

func processAlive(process *os.Process) bool {
	return process.Signal(syscall.Signal(0)) == nil
}

func isAddressInUse(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "address already in use") || strings.Contains(strings.ToLower(err.Error()), "only one usage")
}
