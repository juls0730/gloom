# GLoom

GLoom is a plugin-based web app manager written in Go. GLoom's focus is to provide and simple and efficient way to host micro-web apps easily. Currently, GLoom is a fun little proof of concept, but it suffers from a few issues:

- Incorrectly confgiured plugins will cause GLoom to crash
- GLoom plugins are cannot be reloaded when they are updated

As far as I see it, these issues are unfixable currently, Go Plugins __cannot__ be unloaded, and there's no way to separate GLoom plugins from the host proces, thus meaning if a plugin crashes, GLoom will crash as well.

## Features

- Plugin-based architecture
- RPC-based communication between GLoom and plugins
- Built-in plugin management system
- Built-in plugin management UI

## Getting Started

### Prerequisites

- Go 1.20 or higher
- [zqdgr](https://github.com/juls0730/zqdgr)

### Installation

1. Clone the repository:

```bash
git clone https://github.com/juls0730/gloom.git
```

2. Run the project:

```bash
zqdgr run
```

or if you want to build the project:

```bash
zqdgr build
```

## Usage

please read [DOCUMENTATION.md](DOCUMENTATION.md)

## Contributing

Contributions are welcome!

## License

GLoom is licensed under the MIT License.
