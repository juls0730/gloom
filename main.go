package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/juls0730/gloom/libs"
	"github.com/juls0730/sentinel"
	_ "github.com/mattn/go-sqlite3"
)

type PluginHost struct {
	UnixSocket string
	Process    *os.Process
	Domains    []string
}

type PluginConfig struct {
	// TODO
}

type PreloadPlugin struct {
	File    string   `json:"file"`
	Domains []string `json:"domains"`
}

const DEFAULT_PLUGIN_DIR = "plugs"

type GLoom struct {
	// path to the /tmp directory where the pluginHost binary is unpacked, and where the pluginHost sockets are created
	tmpDir    string
	gloomDir  string
	pluginDir string

	preloadPlugins []PreloadPlugin

	// maps plugin names to plugins
	plugins libs.SyncMap[string, *PluginHost]
	// maps domain names to whether or not they are currently being forwarded to
	hostMap libs.SyncMap[string, bool]

	DB           *sql.DB
	ProxyManager *sentinel.ProxyManager
}

func NewGloom(proxyManager *sentinel.ProxyManager) (*GLoom, error) {
	gloomDir := os.Getenv("GLOOM_DIR")
	if gloomDir == "" {
		if os.Getenv("XDG_DATA_HOME") != "" {
			gloomDir = filepath.Join(os.Getenv("XDG_DATA_HOME"), "gloom")
		} else {
			gloomDir = filepath.Join(os.Getenv("HOME"), ".local/share/gloom")
		}
	}

	gloomDir, err := filepath.Abs(gloomDir)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(gloomDir, 0755); err != nil {
		if os.IsNotExist(err) {
			panic(err)
		}
	}

	db, err := sql.Open("sqlite3", filepath.Join(gloomDir, "gloom.db"))
	if err != nil {
		return nil, err
	}

	schema, err := embeddedAssets.ReadFile("schema.sql")
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(string(schema))
	if err != nil {
		return nil, err
	}

	pluginHost, err := embeddedAssets.ReadFile("dist/host")
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp(os.TempDir(), "gloom")
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(filepath.Join(gloomDir, "pluginHost")); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(gloomDir, "pluginHost"), pluginHost, 0755); err != nil {
			return nil, err
		}

		slog.Debug("Wrote pluginHost", "dir", filepath.Join(gloomDir, "pluginHost"))
	}

	gloom := &GLoom{
		tmpDir:       tmpDir,
		gloomDir:     gloomDir,
		plugins:      libs.SyncMap[string, *PluginHost]{},
		DB:           db,
		ProxyManager: proxyManager,
	}

	if err := gloom.loadConfig(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(gloom.pluginDir, 0755); err != nil {
		return nil, err
	}

	// if gloomi is built into the binary
	if _, err := embeddedAssets.Open("dist/gloomi.so"); err == nil {
		// and if the plugin doesn't exist, copy it over
		// TODO: instead, check if the plugin doesnt exist OR the binary has a newer timestamp than the current version
		if _, err := os.Stat(filepath.Join(gloom.pluginDir, "gloomi.so")); os.IsNotExist(err) {
			gloomiData, err := embeddedAssets.ReadFile("dist/gloomi.so")
			if err != nil {
				return nil, err
			}

			if err := os.WriteFile(filepath.Join(gloom.pluginDir, "gloomi.so"), gloomiData, 0755); err != nil {
				return nil, err
			}
		}
	}

	return gloom, nil
}

func (gloom *GLoom) loadConfig() error {
	configPath := filepath.Join(gloom.gloomDir, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// no config file, write default config
		if err := os.WriteFile(configPath, defaultConfig, 0644); err != nil {
			return nil
		}
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var config any
	_, err = toml.Decode(string(configBytes), &config)
	if err != nil {
		return err
	}

	var ok bool
	gloom.pluginDir, ok = config.(map[string]any)["pluginDir"].(string)
	if !ok || gloom.pluginDir == "" {
		gloom.pluginDir = DEFAULT_PLUGIN_DIR
	}

	gloom.pluginDir = filepath.Join(gloom.gloomDir, gloom.pluginDir)

	proloadPlugins, ok := config.(map[string]any)["plugins"].([]map[string]any)
	if ok {
		for _, plugin := range proloadPlugins {
			file, ok := plugin["file"].(string)
			if !ok {
				return fmt.Errorf("plugin file is not a string")
			}

			_, ok = plugin["domains"].([]any)
			if !ok {
				return fmt.Errorf("plugin domains is not an array")
			}

			domains := make([]string, 0, len(plugin["domains"].([]any)))
			for _, domain := range plugin["domains"].([]any) {
				if _, ok := domain.(string); !ok {
					return fmt.Errorf("plugin domain is not a string")
				}

				domains = append(domains, domain.(string))
			}

			gloom.preloadPlugins = append(gloom.preloadPlugins, PreloadPlugin{
				File:    file,
				Domains: domains,
			})
		}
	}

	return nil
}

func (gloom *GLoom) LoadInitialPlugins() error {
	slog.Info("Loading initial plugins")

	for _, plugin := range gloom.preloadPlugins {
		if err := gloom.RegisterPlugin(filepath.Join(gloom.pluginDir, plugin.File), plugin.File, plugin.Domains); err != nil {
			panic(fmt.Errorf("failed to load preload plugin %s: %w (make sure its in %s)", plugin.File, err, gloom.pluginDir))
		}
	}

	plugins, err := gloom.DB.Query("SELECT path, domains, name FROM plugins")
	if err != nil {
		return err
	}
	defer plugins.Close()

	for plugins.Next() {
		var plugin struct {
			Path   string
			Domain string
			Name   string
		}

		if err := plugins.Scan(&plugin.Path, &plugin.Domain, &plugin.Name); err != nil {
			return err
		}

		domains := strings.Split(plugin.Domain, ",")

		if err := gloom.RegisterPlugin(plugin.Path, plugin.Name, domains); err != nil {
			slog.Warn("Failed to register plugin", "pluginPath", plugin.Path, "error", err)
		}
	}

	slog.Info("Loaded initial plugins")

	return nil
}

var ErrLocked = fmt.Errorf("item is locked")

type MutexLock[T comparable] struct {
	mu       sync.Mutex
	deployed map[T]context.CancelFunc
}

func NewMutexLock[T comparable]() *MutexLock[T] {
	return &MutexLock[T]{
		deployed: make(map[T]context.CancelFunc),
	}
}

func (dt *MutexLock[T]) Lock(id T, ctx context.Context) (context.Context, error) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	// Check if the object is locked
	if _, exists := dt.deployed[id]; exists {
		slog.Debug("Item is locked", "id", id)
		return nil, ErrLocked
	}

	// Create a context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)

	// Store the cancel function
	dt.deployed[id] = cancel

	return ctx, nil
}

func (dt *MutexLock[T]) Unlock(id T) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	// Remove the app from deployed tracking
	if cancel, exists := dt.deployed[id]; exists {
		// Cancel the context
		cancel()
		// Remove from map
		delete(dt.deployed, id)
	}
}

var deploymentLock = NewMutexLock[string]()

func (gloom *GLoom) RegisterPlugin(pluginPath string, name string, domains []string) (err error) {
	slog.Info("Registering plugin", "pluginPath", pluginPath, "domains", domains)

	pathStr := strconv.FormatUint(uint64(rand.Uint64()), 16)
	socketPath := path.Join(gloom.tmpDir, pathStr+".sock")
	controlPath := path.Join(gloom.tmpDir, pathStr+"-control.sock")

	slog.Debug("Starting pluginHost", "pluginPath", pluginPath, "socketPath", socketPath, "controlPath", controlPath)

	processPath := path.Join(gloom.gloomDir, "pluginHost")
	args := []string{pluginPath, socketPath, controlPath}

	cmd := exec.Command(processPath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pluginHost: %w", err)
	}
	process := cmd.Process

	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-timeout:
			_ = process.Signal(os.Interrupt)
			return fmt.Errorf("timed out waiting for pluginHost to start (this is likely a GLoom bug)")
		default:
		}
		_, err := os.Stat(controlPath)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	conn, err := net.DialTimeout("unix", controlPath, 5*time.Second)
	if err != nil {
		_ = process.Signal(os.Interrupt)
		return fmt.Errorf("failed to connect to plugin control socket: %w", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	readTimeout := time.After(30 * time.Second)

	select {
	case <-readTimeout:
		_ = process.Signal(os.Interrupt)
		return fmt.Errorf("timed out waiting for plugin status")
	default:
		status, err := reader.ReadString('\n')
		if err != nil {
			_ = process.Signal(os.Interrupt)
			return fmt.Errorf("error reading plugin status: %w", err)
		}
		status = strings.TrimSpace(status)

		if status == "ready" {
			slog.Debug("PluginHost ported ready", "pluginPath", pluginPath)
			break
		} else if strings.HasPrefix(status, "Error: ") {
			errorMessage := strings.TrimPrefix(status, "Error: ")
			_ = process.Signal(os.Interrupt)
			return fmt.Errorf("plugin reported error: %s", errorMessage)
		} else {
			_ = process.Signal(os.Interrupt)
			return fmt.Errorf("received unknown status from plugin: %s", status)
		}
	}

	proxy, err := sentinel.NewDeploymentProxy(socketPath, NewUnixSocketTransport)
	if err != nil {
		return err
	}

	var oldProxy *sentinel.Proxy
	for _, domain := range domains {
		var ok bool
		if value, exists := gloom.ProxyManager.Load(domain); exists {
			oldProxy = value.(*sentinel.Proxy)
			ok = true
		} else {
			ok = false
		}

		// there can only be one proxy in a set of domains. If a is the domains already attached to the proxy, and b is
		// a superset of a, but the new members of b are not in any other set, then we can be sure there is just one
		if ok {
			break
		}
	}

	// this will replace the old proxy with a new one
	for _, domain := range domains {
		gloom.ProxyManager.AddProxy(domain, proxy)
	}

	plugHost := &PluginHost{
		UnixSocket: socketPath,
		Process:    process,
		Domains:    domains,
	}

	gloom.plugins.Store(name, plugHost)

	if oldProxy != nil {
		go func() {
			slog.Debug("Gracefully shutting down old proxy")
			err := oldProxy.GracefulShutdown(nil)
			if err != nil {
				slog.Warn("Failed to gracefully shutdown old proxy", "error", err)
			}
		}()
	}

	slog.Debug("Registered plugin", "pluginPath", pluginPath, "domains", domains)

	return nil
}

// removes plugin from proxy and kills the process
func (gloom *GLoom) DeletePlugin(pluginName string) error {
	slog.Debug("Deleting plugin", "pluginName", pluginName)

	pluginHost, ok := gloom.plugins.Load(pluginName)
	if !ok {
		return fmt.Errorf("plugin not found")
	}

	for _, domain := range pluginHost.Domains {
		gloom.ProxyManager.RemoveDeployment(domain)
		gloom.hostMap.Store(domain, false)
	}

	pluginHost.Process.Signal(os.Interrupt)
	for _, domain := range pluginHost.Domains {
		gloom.ProxyManager.RemoveDeployment(domain)
	}

	gloom.plugins.Delete(pluginName)

	var pluginPath string
	if err := gloom.DB.QueryRow("SELECT path FROM plugins WHERE name = ?", pluginName).Scan(&pluginPath); err != nil {
		return fmt.Errorf("failed to get plugin path: %w", err)
	}

	if _, err := gloom.DB.Exec("DELETE FROM plugins WHERE name = ?", pluginName); err != nil {
		return fmt.Errorf("failed to delete plugin from database: %w", err)
	}

	if err := os.Remove(pluginPath); err != nil {
		return fmt.Errorf("failed to remove plugin: %w", err)
	}

	return nil
}

func (gloom *GLoom) StartRPCServer() error {
	rpcServer := &GloomRPC{gloom: gloom}
	err := rpc.Register(rpcServer)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", ":7143")
	if err != nil {
		return err
	}

	slog.Info("RPC server running on port 7143\n")
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				fmt.Println("RPC connection error:", err)
				continue
			}
			go rpc.ServeConn(conn)
		}
	}()

	return nil
}

type GloomRPC struct {
	gloom *GLoom
}

type PluginData struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

func (rpc *GloomRPC) ListPlugins(_ struct{}, reply *[]PluginData) error {
	var pluginsArray []PluginData = make([]PluginData, 0, len(rpc.gloom.plugins.Keys()))
	rpc.gloom.plugins.Range(func(key string, value *PluginHost) (shouldContinue bool) {
		pluginData := PluginData{
			Name:    key,
			Domains: value.Domains,
		}
		pluginsArray = append(pluginsArray, pluginData)
		return true
	})

	*reply = pluginsArray
	return nil
}

type PluginUpload struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Data    []byte   `json:"data"`
}

func (rpc *GloomRPC) UploadPlugin(plugin PluginUpload, reply *string) error {
	for _, preloadPlugin := range rpc.gloom.preloadPlugins {
		if plugin.Name == preloadPlugin.File {
			*reply = "Plugin is preloaded"
			return nil
		}
	}

	if plugin.Name == "" {
		*reply = "Plugin name cannot be empty"
		return fmt.Errorf("plugin name cannot be empty")
	}

	for _, char := range plugin.Name {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
			*reply = "Invalid plugin name"
			return fmt.Errorf("invalid plugin name")
		}
	}

	for _, domain := range plugin.Domains {
		if domain == "" {
			*reply = "Domain cannot be empty"
			return fmt.Errorf("domain cannot be empty")
		}
	}

	_, err := deploymentLock.Lock(plugin.Name, context.Background())
	if err != nil && err == ErrLocked {
		*reply = "Plugin is already being updated"
		return fmt.Errorf("plugin is already being updated")
	}
	defer deploymentLock.Unlock(plugin.Name)

	slog.Info("Uploading plugin", "plugin", plugin.Name, "domains", plugin.Domains)
	pluginPath, err := filepath.Abs(filepath.Join(rpc.gloom.pluginDir, (plugin.Name + ".so")))
	if err != nil {
		*reply = "Plugin upload failed"
		return err
	}

	var plugExists bool
	slog.Debug("Checking if plugin exists", "pluginPath", pluginPath, "pluginName", plugin.Name)
	rpc.gloom.DB.QueryRow("SELECT 1 FROM plugins WHERE name = ?", plugin.Name).Scan(&plugExists)
	slog.Debug("Plugin exists", "pluginExists", plugExists)

	var domains []string
	var newDomains []string
	if plugExists {
		// if plugin exists, we need to not check for domains that this plug has already registered, but instead check for new domains this plugin is registering
		domainsMap := map[string]bool{}
		newDomains = make([]string, 0)
		removedDomains := make([]string, 0)
		var sqlDomains string
		err := rpc.gloom.DB.QueryRow("SELECT domains FROM plugins WHERE name = ?", plugin.Name).Scan(&sqlDomains)
		if err != nil {
			return err
		}

		// domains that are already related to the plugin
		existingDomains := strings.Split(sqlDomains, ",")

		for _, domain := range plugin.Domains {
			domainsMap[domain] = true
		}

		for _, domain := range existingDomains {
			if _, ok := domainsMap[domain]; !ok {
				removedDomains = append(removedDomains, domain)
			}
		}

		for _, domain := range removedDomains {
			slog.Debug("Removing domain from plugin", "domain", domain, "plugin", plugin.Name)
			rpc.gloom.ProxyManager.RemoveDeployment(domain)
		}

		for domain := range domainsMap {
			if exists, _ := rpc.gloom.hostMap.Load(domain); !exists {
				newDomains = append(newDomains, domain)
			}

			slog.Debug("Adding domain to plugin", "domain", domain, "plugin", plugin.Name)
			domains = append(domains, domain)
		}
	} else {
		domains = plugin.Domains
		newDomains = plugin.Domains
	}

	for _, domain := range newDomains {
		exists, _ := rpc.gloom.hostMap.Load(domain)
		if exists {
			*reply = fmt.Sprintf("Domain %s already exists", domain)
			return nil
		}
	}

	if _, err := os.Stat(rpc.gloom.pluginDir); os.IsNotExist(err) {
		if err := os.Mkdir(rpc.gloom.pluginDir, 0755); err != nil {
			*reply = "Plugin upload failed"
			return err
		}
	}

	// regardless of if plugin exists or not, we'll upload the file since this could be an update to an existing plugin
	if err := os.WriteFile(pluginPath, plugin.Data, 0644); err != nil {
		*reply = "Plugin upload failed"
		return err
	}

	slog.Debug("Plugin uploaded successfully")

	if err := rpc.gloom.RegisterPlugin(pluginPath, plugin.Name, domains); err != nil {
		os.Remove(pluginPath)
		slog.Warn("Failed to register uplaoded plguin", "pluginPath", pluginPath, "error", err)
		*reply = fmt.Sprintf("Plugin upload failed: %v", err)
		return err
	}

	if plugExists {
		_, err = rpc.gloom.DB.Exec("UPDATE plugins SET domains = ?, path = ? WHERE name = ?", strings.Join(plugin.Domains, ","), pluginPath, plugin.Name)
		if err != nil {
			*reply = fmt.Sprintf("Plugin upload failed: %v", err)
			return err
		}

		*reply = "Plugin updated successfully"
		return nil
	}

	_, err = rpc.gloom.DB.Exec("INSERT INTO plugins (path, name, domains) VALUES (?, ?, ?)", pluginPath, plugin.Name, strings.Join(plugin.Domains, ","))
	if err != nil {
		*reply = fmt.Sprintf("Plugin upload failed: %v", err)
		return err
	}

	*reply = "Plugin uploaded successfully"
	return nil
}

func (rpc *GloomRPC) DeletePlugin(pluginName string, reply *string) error {
	for _, preloadPlugin := range rpc.gloom.preloadPlugins {
		if pluginName == preloadPlugin.File {
			*reply = "Plugin is preloaded"
			return nil
		}
	}

	_, ok := rpc.gloom.plugins.Load(pluginName)
	if !ok {
		*reply = "Plugin not found"
		return nil
	}

	if err := rpc.gloom.DeletePlugin(pluginName); err != nil {
		*reply = fmt.Sprintf("Failed to delete plugin: %v", err)
		return err
	}

	*reply = "Plugin deleted successfully"
	return nil
}

func main() {
	debug, err := strconv.ParseBool(os.Getenv("DEBUG"))
	if err != nil {
		debug = false
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	proxyManager := sentinel.NewProxyManager(RequestLogger{})

	gloom, err := NewGloom(proxyManager)
	if err != nil {
		panic(err)
	}

	if err := gloom.StartRPCServer(); err != nil {
		panic("Failed to start RPC server: " + err.Error())
	}

	gloom.LoadInitialPlugins()

	slog.Info("Server running at http://localhost:3000")
	if err := gloom.ProxyManager.ListenAndServe("127.0.0.1:3000"); err != nil {
		panic(err)
	}
}

type RequestLogger struct{}

func (RequestLogger) LogRequest(app string, status int, latency time.Duration, ip, method, path string) {
	slog.Info("Proxy Request",
		slog.String("time", time.Now().Format(time.RFC3339)),
		slog.Int("status", status),
		slog.Duration("latency", latency),
		slog.String("ip", ip),
		slog.String("method", method),
		slog.String("app", app),
		slog.String("path", path),
	)
}

type unixDialer struct {
	socketPath string
}

// dialContext implements DialContext but ignored everthing and just gives you a connection to the unix socket
func (d *unixDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return net.Dial("unix", d.socketPath)
}

func NewUnixSocketTransport(socket string) *http.Transport {
	return &http.Transport{
		DialContext:         (&unixDialer{socketPath: socket}).DialContext,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 100,
		ForceAttemptHTTP2:   false,
	}
}
