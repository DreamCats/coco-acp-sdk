# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

coco-acp-sdk is a Go SDK for interfacing with coco's ACP (Agent Communication Protocol) server. It provides:

1. **`acp/` package** — Manages the `coco acp serve` subprocess lifecycle: process start/stop, stdio JSON-RPC communication, streaming notifications, crash detection and auto-restart
2. **`daemon/` package** — A Unix socket daemon that keeps the coco acp process alive across CLI invocations, plus a client (`Dial`) for connecting to it

This is an infrastructure library — upper-level agents (like coco-prd) import it to talk to coco without managing the process themselves.

## Build & Test Commands

```bash
go build ./...                    # Build all packages
go test ./... -v                  # Run all tests
go test ./acp/ -v                 # Run acp package tests only
go test ./daemon/ -v              # Run daemon package tests only
go test ./acp/ -run TestPrompt -v # Run a single test
go vet ./...                      # Vet all packages
```

## Architecture

- **`acp/protocol.go`** — JSON-RPC message structs: Request, Response, session types (initialize, session/new, session/prompt, session/update notifications)
- **`acp/client.go`** — Core client: subprocess management via `os/exec`, stdin/stdout pipe communication, `json.Decoder` for multiplexed response routing (by id for results, by method for notifications), `NotifyHandler` callback for streaming chunks/tool calls, `ensureRunning()` for crash auto-recovery, `ServeFlags` for passing CLI flags (--yolo, --allowed-tool, --query-timeout, etc.) to coco acp serve
- **`daemon/protocol.go`** — CLI-to-daemon protocol over Unix socket: Request (prompt/compact/status/shutdown/session_new/session_close/session_list) and Response (chunk/tool_call/done/status/error) types
- **`daemon/server.go`** — Unix socket server: manages multiple sessions via SessionManager, routes requests by sessionId, idle timeout (10min auto-shutdown), `sync.Once` safe shutdown, `ServerOption` for passing ServeFlags
- **`daemon/session.go`** — Session and SessionManager: manages multiple independent ACP sessions, each with its own `*acp.Client`, idle timeout checking
- **`daemon/launcher.go`** — Client-side: `Dial()` connects to daemon (auto-starts if not running), `DialOption` for custom config dir / daemon command / ServeFlags, session management methods (NewSession/CloseSession/ListSessions/UseSession)

**Key design decisions:**
- `CommandFactory` on acp.Client allows test injection (TestHelperProcess pattern)
- `ServeFlags` struct maps to `coco acp` CLI flags (--yolo, --allowed-tool, --disallowed-tool, --bash-tool-timeout, --query-timeout, --config), flows from `DialOption` → `Server` → `SessionManager` → `acp.Client` → subprocess args
- `SetNotifyHandler()` enables per-connection notification routing in daemon
- `waitDone` channel prevents double `proc.Wait()` deadlock
- Each session maps to one `*acp.Client` (one coco acp serve subprocess)
- SessionManager uses `sync.Map` for thread-safe session storage
- Requests are routed by `SessionID` field in protocol

## Key Conventions

- All user-facing messages and error strings are in Chinese
- Config files default to `~/.config/livecoding/coco-acp/`
- Socket/PID files use `0600`/`0700` permissions
- Upper-level agents override paths via `DialOption.ConfigDir`

## Language

This project uses Chinese for all user-facing text, comments, and error messages.
