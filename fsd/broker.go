package fsd

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// Event is a server-sent event pushed to clients.
type Event struct {
	Type    string      `json:"type"`    // "file_push", "broadcast", "message"
	Payload interface{} `json:"payload"` // event-specific data
}

// BroadcastPayload is the payload for a "broadcast" event.
type BroadcastPayload struct {
	File   string `json:"file"`   // filename on server
	Path   string `json:"path"`   // full path on server shared dir
	From   string `json:"from"`   // sender node name
	Group  string `json:"group"`  // target group ("all" or specific)
	Size   int64  `json:"size"`   // file size in bytes
	URL    string `json:"url"`    // download URL (http://server:7700/files/broadcast/...)
}

// Broker manages SSE client connections and broadcasts events.
type Broker struct {
	mu      sync.RWMutex
	clients map[string]chan Event // clientID -> event channel
}

// NewBroker creates a new SSE broker.
func NewBroker() *Broker {
	return &Broker{
		clients: make(map[string]chan Event),
	}
}

// Subscribe registers a client and returns its event channel.
func (b *Broker) Subscribe(clientID string) chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Close existing channel if client reconnects
	if old, ok := b.clients[clientID]; ok {
		close(old)
	}

	ch := make(chan Event, 64)
	b.clients[clientID] = ch
	log.Printf("fsd: SSE client connected: %s (total: %d)", clientID, len(b.clients))
	return ch
}

// Unsubscribe removes a client.
func (b *Broker) Unsubscribe(clientID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.clients[clientID]; ok {
		close(ch)
		delete(b.clients, clientID)
	}
	log.Printf("fsd: SSE client disconnected: %s (total: %d)", clientID, len(b.clients))
}

// Publish sends an event to specific clients or all clients.
func (b *Broker) Publish(event Event, targetIDs ...string) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(targetIDs) == 0 {
		// Broadcast to all
		for id, ch := range b.clients {
			select {
			case ch <- event:
			default:
				log.Printf("fsd: SSE channel full for %s, dropping event", id)
			}
		}
		return
	}

	// Send to specific clients
	targets := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		targets[id] = true
	}
	for id, ch := range b.clients {
		if targets[id] {
			select {
			case ch <- event:
			default:
				log.Printf("fsd: SSE channel full for %s, dropping event", id)
			}
		}
	}
}

// ConnectedClients returns the list of connected client IDs.
func (b *Broker) ConnectedClients() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ids := make([]string, 0, len(b.clients))
	for id := range b.clients {
		ids = append(ids, id)
	}
	return ids
}

// HandleSSE is the HTTP handler for the /events SSE endpoint.
func (b *Broker) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Use caller identity as client ID
	nodeName, _, _ := CallerInfo(r)
	if nodeName == "" {
		nodeName = r.RemoteAddr
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := b.Subscribe(nodeName)
	defer b.Unsubscribe(nodeName)

	// Send initial "connected" event
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"ok\"}\n\n")
	flusher.Flush()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return // channel closed (reconnect or shutdown)
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
