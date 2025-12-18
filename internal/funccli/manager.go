package funccli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	funcfn "knative.dev/func/pkg/functions"
)

const (
	githubAPIURL         = "https://api.github.com/repos/knative/func/releases/latest"
	defaultCheckInterval = 5 * time.Minute
	binaryName           = "func"
)

type Manager interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)

	Describe(ctx context.Context, name, namespace string) (funcfn.Instance, error)
	Deploy(ctx context.Context, repoPath string, namespace string, opts DeployOptions) error

	GetCurrentVersion(ctx context.Context) (string, error)
	GetLatestMiddlewareVersion(ctx context.Context, runtime, invoke string) (string, error)
	GetMiddlewareVersion(ctx context.Context, name, namespace string) (string, error)
}

type DeployOptions struct {
	Registry         string
	InsecureRegistry bool
	RegistryAuthFile string

	GitUrl string

	Builder string
}

var _ Manager = &managerImpl{}

// managerImpl handles periodic checks and downloads of the Knative func CLI binary
type managerImpl struct {
	logger           logr.Logger
	checkInterval    time.Duration
	installPath      string
	mu               sync.Mutex
	httpClient       *http.Client
	disableCLIUpdate bool
}

// GitHubRelease represents the GitHub API response for a release
type GitHubRelease struct {
	Name   string `json:"name"`
	Assets []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// NewManager creates a new func CLI manager
func NewManager(logger logr.Logger, installPath string, checkInterval time.Duration, disableCLIUpdate bool) (*managerImpl, error) {
	if installPath == "" {
		// Default to a temporary directory
		installPath = filepath.Join(os.TempDir(), "func-operator", "bin")
	}

	if checkInterval == 0 {
		checkInterval = defaultCheckInterval
	}

	// Ensure install directory exists
	if err := os.MkdirAll(installPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create install directory for func cli: %w", err)
	}

	if disableCLIUpdate {
		// ensure binary exists already
		cliPath := filepath.Join(installPath, binaryName)
		file, err := os.Stat(cliPath)
		if err != nil {
			return nil, fmt.Errorf("failed to determine if binary exists already: %w", err)
		}
		if !file.Mode().IsRegular() {
			return nil, fmt.Errorf("%s binary is not a regular file", cliPath)
		}
	}

	return &managerImpl{
		logger:        logger.WithName("funccli-manager"),
		checkInterval: checkInterval,
		installPath:   installPath,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		disableCLIUpdate: disableCLIUpdate,
	}, nil
}

// Start implements the manager.Runnable interface
func (m *managerImpl) Start(ctx context.Context) error {
	if m.disableCLIUpdate {
		m.logger.Info("Skipping updating funccli manager as CLI updates are disabled")
		return nil
	}

	m.logger.Info("Starting func CLI manager", "checkInterval", m.checkInterval, "installPath", m.installPath)

	// Perform initial check immediately
	if err := m.checkAndUpdate(ctx); err != nil {
		m.logger.Error(err, "Initial func CLI check failed, will retry on next interval")
	}

	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("Stopping func CLI manager")
			return nil
		case <-ticker.C:
			if err := m.checkAndUpdate(ctx); err != nil {
				// TODO: think, if we want to cancel/error the context and mark everything as failed then
				m.logger.Error(err, "Failed to check/update func CLI")
			}
		}
	}
}

// GetBinaryPath returns the path to the installed func binary
func (m *managerImpl) GetBinaryPath() (string, error) {
	binaryPath := filepath.Join(m.installPath, binaryName)

	// Check if binary exists
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		return "", fmt.Errorf("binary %s does not exist", binaryPath)
	}

	return binaryPath, nil
}

// GetCurrentVersion returns the currently installed version by running "func version"
func (m *managerImpl) GetCurrentVersion(ctx context.Context) (string, error) {
	output, err := m.Run(ctx, "", "version", "-v")
	if err != nil {
		return "", fmt.Errorf("failed to get version: %w", err)
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Knative:") {
			parts := strings.Split(line, " ")
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}

	return "", fmt.Errorf("failed to get version from func binary")
}

// EnsureReady ensures the func CLI is downloaded and ready to use.
// This should be called before controllers start processing resources.
func (m *managerImpl) EnsureReady(ctx context.Context) error {
	m.logger.Info("Ensuring func CLI is ready")
	return m.checkAndUpdate(ctx)
}

func (m *managerImpl) Run(ctx context.Context, dir string, args ...string) (string, error) {
	bin, err := m.GetBinaryPath()
	if err != nil {
		return "", fmt.Errorf("failed to get binary path: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("failed to run func %s: %q. %w", strings.Join(cmd.Args, " "), output, err)
	}

	return string(output), nil
}

func (m *managerImpl) Describe(ctx context.Context, name, namespace string) (funcfn.Instance, error) {
	out, err := m.Run(ctx, "", "describe", "-n", namespace, "-o", "json", name)
	if err != nil {
		return funcfn.Instance{}, fmt.Errorf("failed to describe function: %q. %w", out, err)
	}

	instance := funcfn.Instance{}

	if err := json.Unmarshal([]byte(out), &instance); err != nil {
		return funcfn.Instance{}, fmt.Errorf("failed to unmarshal func describe output: %w", err)
	}

	return instance, nil
}

func (m *managerImpl) Deploy(ctx context.Context, repoPath string, namespace string, opts DeployOptions) error {
	deployArgs := []string{
		"deploy",
		"--remote",
		"--namespace", namespace,
		"--registry", opts.Registry,
		"--git-url", opts.GitUrl,
		"--builder", opts.Builder,
		"--registry-authfile", opts.RegistryAuthFile,
	}

	if opts.InsecureRegistry {
		deployArgs = append(deployArgs, "--insecure-registry")
	}

	out, err := m.Run(ctx, repoPath, deployArgs...)
	if err != nil {
		return fmt.Errorf("failed to deploy function: %q. %w", out, err)
	}

	return nil
}

func (m *managerImpl) GetLatestMiddlewareVersion(ctx context.Context, runtime string, invoke string) (string, error) {
	versions := struct {
		MiddlewareVersions map[string]map[string]string `json:"middlewareVersions,omitempty"`
	}{}

	if invoke == "" {
		invoke = "http"
	}

	out, err := m.Run(ctx, "", "version", "-v", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("failed to get latest middleware version: %w", err)
	}

	if err := json.Unmarshal([]byte(out), &versions); err != nil {
		return "", fmt.Errorf("failed to unmarshal latest middleware version: %w", err)
	}

	runtimeVersions, ok := versions.MiddlewareVersions[runtime]
	if !ok {
		return "", fmt.Errorf("failed to find latest middleware version for runtime %s", runtime)
	}

	middlewareVersion, ok := runtimeVersions[invoke]
	if !ok {
		return "", fmt.Errorf("failed to find latest middleware version for runtime %s with %s invoke type", runtime, invoke)
	}

	return middlewareVersion, nil
}

func (m *managerImpl) GetMiddlewareVersion(ctx context.Context, name string, namespace string) (string, error) {
	instance, err := m.Describe(ctx, namespace, name)
	if err != nil {
		return "", fmt.Errorf("failed to describe function: %w", err)
	}

	return instance.Middleware.Version, nil
}

// checkAndUpdate checks for a new version and downloads it if available
func (m *managerImpl) checkAndUpdate(ctx context.Context) error {
	if m.disableCLIUpdate {
		m.logger.Info("Skipping updating funccli manager as CLI updates are disabled")
		return nil
	}

	// Lock to ensure only one update happens at a time
	m.mu.Lock()
	defer m.mu.Unlock()

	latestRelease, err := m.getLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest release: %w", err)
	}

	// Get currently installed version by running "func version"
	currentVersion, err := m.GetCurrentVersion(ctx)
	if err != nil {
		m.logger.V(1).Info("Failed to get current version, will download latest", "error", err)
		currentVersion = ""
	}

	if currentVersion == latestRelease.Name {
		m.logger.V(1).Info("Already on latest version", "version", currentVersion)
		return nil
	}

	m.logger.Info("New version available", "current", currentVersion, "latest", latestRelease.Name)

	if err := m.downloadAndInstall(ctx, latestRelease); err != nil {
		return fmt.Errorf("failed to download and install: %w", err)
	}

	m.logger.Info("Successfully updated func CLI", "version", latestRelease.Name)
	return nil
}

// getLatestRelease fetches the latest release information from GitHub
func (m *managerImpl) getLatestRelease(ctx context.Context) (*GitHubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubAPIURL, nil)
	if err != nil {
		return nil, err
	}

	// Add GitHub API headers
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// downloadAndInstall downloads the appropriate binary and installs it
func (m *managerImpl) downloadAndInstall(ctx context.Context, release *GitHubRelease) error {
	// Determine the appropriate asset name based on OS and architecture
	assetName := m.getAssetName()

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no suitable asset found for %s/%s", goruntime.GOOS, goruntime.GOARCH)
	}

	m.logger.Info("Downloading func CLI", "url", downloadURL, "asset", assetName)

	// Download to temporary file first
	tmpFile := filepath.Join(m.installPath, fmt.Sprintf(".%s.tmp", binaryName))
	if err := m.downloadFile(ctx, downloadURL, tmpFile); err != nil {
		return err
	}

	// Make it executable
	if err := os.Chmod(tmpFile, 0755); err != nil {
		if err := os.Remove(tmpFile); err != nil {
			m.logger.Error(err, "Failed to remove tmp file while file could not be marked as executable", "file", tmpFile)
		}

		return fmt.Errorf("failed to make binary executable: %w", err)
	}

	// Atomic rename to final location
	finalPath := filepath.Join(m.installPath, binaryName)
	if err := os.Rename(tmpFile, finalPath); err != nil {
		if err := os.Remove(tmpFile); err != nil {
			m.logger.Error(err, "Failed to remove tmp file while file could not be renamed", "file", tmpFile)
		}

		return fmt.Errorf("failed to install binary: %w", err)
	}

	return nil
}

// downloadFile downloads a file from the given URL
func (m *managerImpl) downloadFile(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// getAssetName returns the appropriate asset name for the current platform
func (m *managerImpl) getAssetName() string {
	goos := goruntime.GOOS
	goarch := goruntime.GOARCH

	// Knative func uses naming like: func_linux_amd64
	return fmt.Sprintf("func_%s_%s", goos, goarch)
}
