# GLoom Documentation

## Plugins

> [!IMPORTANT]  
> Plugins __must__ be compiled with the same version of Go that GLoom was compiled with. This is a limitation of Golang's plugin system, which is what GLoom uses to load plugins.

Plugins are the core of GLoom, they are responsible for handling requests and providing routes. When building a plugin, it's expected that all the assets you need will be bundled with the plugin. However, you are allowed to create directories for assets like file uploads, but we urge you to create a specific directory for assets (ie a database or a public directory) to avoid cluttering the root directory of gloom.

### Plugin Interface

The `Plugin` interface is the main interface for plugins to implement. It has three methods:

- `Init() (*fiber.Config, error)` - This method is called when the plugin is loaded. It is the function that is initially called when the plugin is loaded.
- `RegisterRoutes(router fiber.Router)` - This method is called when the plugin is loaded and is responsible for registering routes to the router.

Furthermore, your plugin should export a symbol named `Plugin` that implements the `Plugin` interface. The easiest way to do this in Go is simply

```go
var Plugin MyPluginDataStruct
```

An example plugin is provided in the `plugin` directory and can be built using the following command:

```bash
zqdgr build
```

This will generate a `plugin.so` file in the `plugin` directory.

## RPC

GLoom exposes an RPC server on port 7143. This server is used for GLoom's plugin management system. Gloom currently provides three methods:

- `ListPlugins(struct{}, reply *[]PluginData) error` - This method returns a list of all registered plugins and their domains.
- `UploadPlugin(PluginUpload, reply *string) error` - This method uploads a plugin to GLoom. 
- `DeletePlugin(pluginName string, reply *string) error` - This method deletes a plugin from GLoom.

PluginData is a struct that looks like this:

```go
type PluginData struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}
```

PluginUpload is a struct that looks like this:

```go
type PluginUpload struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Data    []byte   `json:"data"`
}
```

> [!IMPORTANT]
> The Name field can only contain alphanumeric characters and underscores and dashes.

## GLoomI

GLoomI is the included plugin management interface for GLoom, it utilizes the GLoom RPC, much like you would if you wanted to make your own management interface. By default, GLoomI is configured to use 127.0.0.1 as the hostname, but you con configure it to use a different hostname by setting the `GLOOMI_HOSTNAME` environment variable. The endpoints for GLoomI are as follows:

- `GET /api/plugins` - This endpoint returns a list of all registered plugins and their domains.
- `POST /api/plugins` - This endpoint uploads a plugin to GLoom. it takes a multipart/form-data request with the following fields:
  - `plugin` - The plugin file to upload.
  - `domains` - A comma-separated list of domains to associate with the plugin.
- `DELETE /api/plugins/:pluginName` - This endpoint deletes a plugin from GLoom. `pluginName` is the string returned by the `Name()` method of the plugin and is case-sensitive.
