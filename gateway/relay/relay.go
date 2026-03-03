package relay

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Session represents a WebSocket relay session between frontend and worker
type Session struct {
	JobID      string
	FrontendWS *websocket.Conn
	WorkerWS   *websocket.Conn
	mu         sync.RWMutex
	buffer     [][]byte // Buffer messages when one side isn't connected yet
	done       chan struct{}
	createdAt  time.Time
}

// Manager manages all active relay sessions
type Manager struct {
	sessions sync.Map // map[string]*Session
}

func NewManager() *Manager {
	return &Manager{}
}

// GetOrCreate returns existing session or creates a new one
func (m *Manager) GetOrCreate(jobID string) *Session {
	if s, ok := m.sessions.Load(jobID); ok {
		return s.(*Session)
	}
	s := &Session{
		JobID:     jobID,
		done:      make(chan struct{}),
		createdAt: time.Now(),
	}
	actual, _ := m.sessions.LoadOrStore(jobID, s)
	return actual.(*Session)
}

// Get returns a session if it exists
func (m *Manager) Get(jobID string) *Session {
	if s, ok := m.sessions.Load(jobID); ok {
		return s.(*Session)
	}
	return nil
}

// Remove cleans up a session
func (m *Manager) Remove(jobID string) {
	if s, ok := m.sessions.LoadAndDelete(jobID); ok {
		sess := s.(*Session)
		close(sess.done)
	}
}

// SetFrontend sets the frontend WebSocket connection and replays buffered messages
func (s *Session) SetFrontend(conn *websocket.Conn) {
	s.mu.Lock()
	s.FrontendWS = conn
	buffered := make([][]byte, len(s.buffer))
	copy(buffered, s.buffer)
	s.buffer = nil
	s.mu.Unlock()

	// Replay buffered messages from worker
	for _, msg := range buffered {
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}

// SetWorker sets the worker WebSocket connection
func (s *Session) SetWorker(conn *websocket.Conn) {
	s.mu.Lock()
	s.WorkerWS = conn
	s.mu.Unlock()
}

// RelayWorkerToFrontend reads from worker WS and forwards to frontend
func (s *Session) RelayWorkerToFrontend(onComplete func(outputURLs []string, durationMs int64), onError func(errMsg string)) {
	defer func() {
		s.mu.RLock()
		if s.WorkerWS != nil {
			s.WorkerWS.Close()
		}
		s.mu.RUnlock()
	}()

	for {
		select {
		case <-s.done:
			return
		default:
		}

		s.mu.RLock()
		ws := s.WorkerWS
		s.mu.RUnlock()

		if ws == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		_, message, err := ws.ReadMessage()
		if err != nil {
			log.Printf("[relay] Worker WS read error for job %s: %v", s.JobID, err)
			return
		}

		// Check if this is a completion or error message from worker
		var msg map[string]interface{}
		if json.Unmarshal(message, &msg) == nil {
			if msgType, ok := msg["type"].(string); ok {
				switch msgType {
				case "completed":
					var urls []string
					if rawURLs, ok := msg["output_urls"].([]interface{}); ok {
						for _, u := range rawURLs {
							if s, ok := u.(string); ok {
								urls = append(urls, s)
							}
						}
					}
					var dur int64
					if d, ok := msg["duration_ms"].(float64); ok {
						dur = int64(d)
					}
					onComplete(urls, dur)
				case "error":
					errMsg := "unknown error"
					if e, ok := msg["error"].(string); ok {
						errMsg = e
					}
					onError(errMsg)
				}
			}
		}

		// Forward to frontend
		s.mu.RLock()
		frontendWS := s.FrontendWS
		s.mu.RUnlock()

		if frontendWS != nil {
			if err := frontendWS.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[relay] Frontend WS write error for job %s: %v", s.JobID, err)
			}
		} else {
			// Buffer for when frontend connects
			s.mu.Lock()
			s.buffer = append(s.buffer, message)
			s.mu.Unlock()
		}
	}
}

// Done returns the done channel
func (s *Session) Done() <-chan struct{} {
	return s.done
}
