package main

import (
	"bufio"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/rpc"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joho/godotenv"
	"github.com/juls0730/gloom/libs"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql host
var embeddedAssets embed.FS

type PluginHost struct {
	UnixSocket string
	Process    *os.Process
	Domains    []string
}

type PreloadPlugin struct {
	File    string   `json:"file"`
	Domains []string `json:"domains"`
}

type GLoom struct {
	// path to the pluginHost binary
	tmpDir    string
	pluginDir string

	preloadPlugins []PreloadPlugin

	plugins libs.SyncMap[string, *PluginHost]
	hostMap libs.SyncMap[string, bool]

	DB           *sql.DB
	ProxyManager *ProxyManager
}

func NewGloom(proxyManager *ProxyManager) (*GLoom, error) {
	pluginsDir := os.Getenv("PLUGINS_DIR")
	if pluginsDir == "" {
		pluginsDir = "plugs"
	}

	pluginsDir, err := filepath.Abs(pluginsDir)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		if os.IsNotExist(err) {
			panic(err)
		}
	}

	db, err := sql.Open("sqlite3", "gloom.db")
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

	pluginHost, err := embeddedAssets.ReadFile("host")
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp(os.TempDir(), "gloom")
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(tmpDir+"/pluginHost", pluginHost, 0755); err != nil {
		return nil, err
	}
	slog.Debug("Wrote pluginHost", "dir", tmpDir+"/pluginHost")

	var preloadPlugins []PreloadPlugin
	preloadPluginsEnv, ok := os.LookupEnv("PRELOAD_PLUGINS")
	if ok {
		err = json.Unmarshal([]byte(preloadPluginsEnv), &preloadPlugins)
		if err != nil {
			panic(err)
		}
	} else {
		preloadPlugins = []PreloadPlugin{
			{
				File:    "gloomi.so",
				Domains: []string{"localhost"},
			},
		}
	}

	gloom := &GLoom{
		tmpDir:         tmpDir,
		pluginDir:      pluginsDir,
		preloadPlugins: preloadPlugins,
		plugins:        libs.SyncMap[string, *PluginHost]{},
		DB:             db,
		ProxyManager:   proxyManager,
	}

	return gloom, nil
}

func (gloom *GLoom) LoadInitialPlugins() error {
	slog.Debug("Loading initial plugins")

	for _, plugin := range gloom.preloadPlugins {
		if err := gloom.RegisterPlugin(filepath.Join(gloom.pluginDir, plugin.File), plugin.File, plugin.Domains); err != nil {
			slog.Warn("Failed to register plugin", "pluginPath", plugin.File, "error", err)
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

	slog.Debug("Starting pluginHost", "pluginPath", pluginPath, "socketPath", socketPath)

	processPath := path.Join(gloom.tmpDir, "pluginHost")
	args := []string{pluginPath, socketPath, controlPath}
	slog.Debug("Starting pluginHost", "args", args)

	cmd := exec.Command(processPath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pluginHost: %w", err)
	}
	process := cmd.Process

	for {
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

	proxy, err := NewDeploymentProxy(socketPath)
	if err != nil {
		return err
	}

	var oldProxy *Proxy
	for _, domain := range domains {
		var ok bool
		oldProxy, ok = gloom.ProxyManager.Load(domain)
		// there can only be one in a set of domains. If a is the domains already attached to the proxy, and b is
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
			oldProxy.GracefulShutdown(nil)
		}()
	}

	slog.Debug("Registered plugin", "pluginPath", pluginPath, "domains", domains)

	return nil
}

// removes plugin from proxy and kills the process
func (gloom *GLoom) DeletePlugin(pluginName string) error {
	slog.Debug("Deleting plugin", "pluginName", pluginName)

	plug, ok := gloom.plugins.Load(pluginName)
	if !ok {
		return fmt.Errorf("plugin not found")
	}

	for _, domain := range plug.Domains {
		gloom.ProxyManager.RemoveDeployment(domain)
		gloom.hostMap.Store(domain, false)
	}

	plug.Process.Signal(os.Interrupt)
	for _, domain := range plug.Domains {
		gloom.ProxyManager.RemoveDeployment(domain)
	}

	gloom.plugins.Delete(pluginName)

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

	fmt.Printf("RPC server running on port 7143\n")
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
	pluginPath, err := filepath.Abs(fmt.Sprintf("plugs/%s", plugin.Name))
	if err != nil {
		*reply = "Plugin upload failed"
		return err
	}

	var plugExists bool
	// TODO: make name a consistent identifier
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
		_, ok := rpc.gloom.hostMap.Load(domain)
		if ok {
			*reply = fmt.Sprintf("Domain %s already exists", domain)
			return nil
		}
	}

	plugsDir := "plugs"

	if os.Getenv("PLUGINS_DIR") != "" {
		plugsDir = os.Getenv("PLUGINS_DIR")
	}

	if _, err := os.Stat(plugsDir); os.IsNotExist(err) {
		if err := os.Mkdir(plugsDir, 0755); err != nil {
			*reply = "Plugin upload failed"
			return err
		}
	}

	// regardless of if plugin exists or not, we'll upload the file since this could be an update to an existing plugin
	if err := os.WriteFile(pluginPath, plugin.Data, 0644); err != nil {
		*reply = "Plugin upload failed"
		return err
	}

	fmt.Println("Plugin uploaded successfully")

	if err := rpc.gloom.RegisterPlugin(pluginPath, plugin.Name, domains); err != nil {
		os.Remove(pluginPath)
		slog.Warn("Failed to register uplaoded plguin", "pluginPath", pluginPath, "error", err)
		*reply = fmt.Sprintf("Plugin upload failed: %v", err)
		return err
	}

	if !plugExists {
		_, err = rpc.gloom.DB.Exec("INSERT INTO plugins (path, name, domains) VALUES (?, ?, ?)", pluginPath, plugin.Name, strings.Join(plugin.Domains, ","))
		if err != nil {
			*reply = fmt.Sprintf("Plugin upload failed: %v", err)
			return err
		}
	} else {
		_, err = rpc.gloom.DB.Exec("UPDATE plugins SET domains = ?, path = ? WHERE name = ?", strings.Join(plugin.Domains, ","), pluginPath, plugin.Name)
		if err != nil {
			*reply = fmt.Sprintf("Plugin upload failed: %v", err)
			return err
		}
	}

	if plugExists {
		// exit out early otherwise we risk creating multiple of the same plugin and causing undefined behavior
		*reply = "Plugin updated successfully"
		return nil
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

	_, err := rpc.gloom.DB.Exec("DELETE FROM plugins WHERE name = ?", pluginName)
	if err != nil {
		*reply = "Plugin not found"
		return err
	}

	rpc.gloom.DeletePlugin(pluginName)

	*reply = "Plugin deleted successfully"
	return nil
}

func init() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found")
	}
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

	proxyManager := NewProxyManager()

	gloom, err := NewGloom(proxyManager)
	if err != nil {
		panic(err)
	}

	if err := gloom.StartRPCServer(); err != nil {
		panic("Failed to start RPC server: " + err.Error())
	}

	gloom.LoadInitialPlugins()

	fmt.Println("Server running at http://localhost:3000")
	if err := gloom.ProxyManager.ListenAndServe("127.0.0.1:3000"); err != nil {
		panic(err)
	}
}

// this is the object that oversees the proxying of requests to the correct deployment
type ProxyManager struct {
	libs.SyncMap[string, *Proxy]
}

func NewProxyManager() *ProxyManager {
	return &ProxyManager{}
}

func (proxyManager *ProxyManager) ListenAndServe(host string) error {
	slog.Info("Proxy server starting", "url", host)
	if err := http.ListenAndServe(host, proxyManager); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start proxy server: %v", err)
	}
	return nil
}

// Stops forwarding traffic to a deployment
func (proxyManager *ProxyManager) RemoveDeployment(host string) {
	slog.Info("Removing proxy", "host", host)
	proxyManager.Delete(host)
}

// Starts forwarding traffic to a deployment. The deployment must be ready to recieve requests before this is called.
func (proxyManager *ProxyManager) AddProxy(host string, proxy *Proxy) {
	slog.Debug("Adding proxy", "host", host)
	proxyManager.Store(host, proxy)
}

// This function is responsible for taking an http request and forwarding it to the correct deployment
func (proxyManager *ProxyManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	host := r.Host
	path := r.URL.Path
	method := r.Method
	ip := getClientIP(r)

	slog.Debug("Proxying request", "host", host, "path", path, "method", method, "ip", ip)
	proxy, ok := proxyManager.Load(host)
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		logRequest(host, http.StatusNotFound, time.Since(start), ip, method, path)
		return
	}

	// Create a custom ResponseWriter to capture the status code
	rw := &ResponseWriterInterceptor{ResponseWriter: w, statusCode: http.StatusOK}

	proxy.proxyFunc.ServeHTTP(rw, r)

	latency := time.Since(start)
	statusCode := rw.statusCode

	logRequest(host, statusCode, latency, ip, method, path)
}

// getClientIP retrieves the client's IP address from the request.
// It handles cases where the IP might be forwarded by proxies.
func getClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return forwarded
	}
	return r.RemoteAddr
}

// ResponseWriterInterceptor is a custom http.ResponseWriter that captures the status code.
type ResponseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
}

func (rw *ResponseWriterInterceptor) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func logRequest(app string, status int, latency time.Duration, ip, method, path string) {
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

func NewUnixSocketTransport(socketPath string) *http.Transport {
	return &http.Transport{
		DialContext: (&unixDialer{socketPath: socketPath}).DialContext,
	}
}

type Proxy struct {
	socket          string
	proxyFunc       *httputil.ReverseProxy
	shutdownTimeout time.Duration
	activeRequests  int64
}

const PROXY_SHUTDOWN_TIMEOUT = 30 * time.Second

// Creates a proxy for a given deployment
func NewDeploymentProxy(socket string) (*Proxy, error) {
	proxy := &Proxy{
		socket:          socket,
		shutdownTimeout: PROXY_SHUTDOWN_TIMEOUT,
		activeRequests:  0,
	}

	transport := &http.Transport{
		DialContext:         (&unixDialer{socketPath: socket}).DialContext,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 100,
		ForceAttemptHTTP2:   false,
	}

	proxy.proxyFunc = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = &url.URL{
				Scheme: "http",
				Host:   req.Host,
				Path:   req.URL.Path,
			}
			atomic.AddInt64(&proxy.activeRequests, 1)
		},
		Transport: transport,
		ModifyResponse: func(resp *http.Response) error {
			atomic.AddInt64(&proxy.activeRequests, -1)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("Proxy error", "error", err)
			atomic.AddInt64(&proxy.activeRequests, -1)
			w.WriteHeader(http.StatusInternalServerError)
		},
	}

	return proxy, nil
}

func (p *Proxy) GracefulShutdown(shutdownFunc func()) {
	slog.Debug("Shutting down proxy", "socket", p.socket)

	ctx, cancel := context.WithTimeout(context.Background(), p.shutdownTimeout)
	defer cancel()

	done := false
	for !done {
		select {
		case <-ctx.Done():
			slog.Debug("Proxy shutdown timed out", "socket", p.socket)

			done = true
		default:
			if atomic.LoadInt64(&p.activeRequests) == 0 {
				slog.Debug("Proxy shutdown completed successfully", "socket", p.socket)
				done = true
			}

			time.Sleep(time.Second)
		}
	}

	if shutdownFunc != nil {
		shutdownFunc()
	}
}
