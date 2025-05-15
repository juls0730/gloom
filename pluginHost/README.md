# Plugin Host
This is the plugin host for GLoom. This is a small program that is responsible for loading and managing plugins. It is responsible for starting the plugin and forwarding requests to it. This is meant to be used with GLoom, but can be used as a standalone program if you so choose. The Plugin Host is built automatically when you build GLoom via `zqdgr build`.

## Building
To build the plugin host standalone, run the following command in the `pluginHost` directory:

```bash
zqdgr build
```

or run the following command in the project root:

```bash
zqdgr build:pluginHost
```

## Running
To run the plugin host, run the following command:

```bash
./host <pluginPath> <socketPath> [controlPath]
```

- `pluginPath` - The path to the plugin to load.
- `socketPath` - The path to the socket that the plugin will use to listen for http requests through.
- `controlPath` - (Optional) The path to the control socket. If not provided, the host will not send errors or status messages to the control socket and instead log them to stdout and stderr.