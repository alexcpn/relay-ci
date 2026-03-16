package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
)

// httpTransport implements the MCP Streamable HTTP transport.
// POST /mcp  → JSON-RPC request/response (or SSE stream for notifications)
// GET  /mcp  → SSE stream for server-initiated messages
// DELETE /mcp → close session
type httpTransport struct {
	server *mcpServer
	logger *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*sseSession
}

type sseSession struct {
	id      string
	msgCh   chan []byte // server-initiated messages
	closeCh chan struct{}
}

func newHTTPTransport(server *mcpServer, logger *slog.Logger) *httpTransport {
	return &httpTransport{
		server:   server,
		logger:   logger,
		sessions: make(map[string]*sseSession),
	}
}

func (h *httpTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers for browser-based agents.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Mcp-Session-Id")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodGet:
		h.handleSSE(w, r)
	case http.MethodDelete:
		h.handleDelete(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePost processes a JSON-RPC request and returns the response.
// For initialize requests, creates a new session.
func (h *httpTransport) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}

	h.logger.Info("HTTP request", "method", req.Method, "id", string(req.ID))

	// Handle the request.
	resp := h.server.handleRequest(req)

	// For initialize, create a session and return the session ID.
	if req.Method == "initialize" {
		sessionID := generateSessionID()
		session := &sseSession{
			id:      sessionID,
			msgCh:   make(chan []byte, 64),
			closeCh: make(chan struct{}),
		}
		h.mu.Lock()
		h.sessions[sessionID] = session
		h.mu.Unlock()

		w.Header().Set("Mcp-Session-Id", sessionID)
		h.logger.Info("session created", "session_id", sessionID)
	} else {
		// Echo back session ID if provided.
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			w.Header().Set("Mcp-Session-Id", sid)
		}
	}

	// Notifications have no response.
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleSSE opens a Server-Sent Events stream for server-initiated messages.
func (h *httpTransport) handleSSE(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	session, ok := h.sessions[sessionID]
	h.mu.RUnlock()

	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	h.logger.Info("SSE stream opened", "session_id", sessionID)

	for {
		select {
		case msg, ok := <-session.msgCh:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-session.closeCh:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// handleDelete closes a session.
func (h *httpTransport) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	session, ok := h.sessions[sessionID]
	if ok {
		close(session.closeCh)
		delete(h.sessions, sessionID)
	}
	h.mu.Unlock()

	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	h.logger.Info("session closed", "session_id", sessionID)
	w.WriteHeader(http.StatusOK)
}

func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
