# Code Statistics

| Category | Count |
|---|---|
| **Tracked files** | 102 |
| **Go source (hand-written)** | 12,485 lines (54 files) |
| **Go source (generated, `gen/`)** | 8,838 lines |
| **Go test files** | 17 files, 4,057 lines |
| **Proto definitions** | 796 lines (7 files) |
| **Markdown docs** | 10 files |
| **YAML configs** | 3 files |

The codebase is primarily Go (~12.5k lines of hand-written code), with about 33% of that being tests. The `gen/` directory holds auto-generated gRPC/protobuf code. The project structure spans `cmd/` (binaries: master, worker, cli, mcp), `pkg/` (libraries: dag, scm, pipeline, container, auth, etc.), and `proto/` (service definitions).
