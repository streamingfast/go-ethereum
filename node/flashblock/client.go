package flashblock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/ethereum/go-ethereum/log"
	"github.com/gorilla/websocket"
)

// Client represents a flashblock WebSocket client with automatic reconnection
type Client struct {
	url     string
	headers http.Header
	conn    *websocket.Conn
	logger  log.Logger

	// Reconnection configuration
	maxRetries       int
	baseBackoff      time.Duration
	handshakeTimeout time.Duration
	readTimeout      time.Duration
}

// NewClient creates a new flashblock client with default reconnection settings
func NewClient(url string, headers http.Header, logger log.Logger) *Client {
	return &Client{
		url:              url,
		headers:          headers,
		logger:           logger,
		maxRetries:       3,
		baseBackoff:      time.Second,
		handshakeTimeout: 10 * time.Second,
		readTimeout:      30 * time.Second,
	}
}

// WithReconnectionConfig configures reconnection behavior
func (c *Client) WithReconnectionConfig(maxRetries int, baseBackoff time.Duration) *Client {
	c.maxRetries = maxRetries
	c.baseBackoff = baseBackoff
	return c
}

// WithReadTimeout configures the read timeout for receiving messages
func (c *Client) WithReadTimeout(timeout time.Duration) *Client {
	c.readTimeout = timeout
	return c
}

// Connect establishes a WebSocket connection to the flashblock provider
func (c *Client) Connect(ctx context.Context) error {
	return c.connectWithRetry(ctx)
}

// connectWithRetry attempts to connect with exponential backoff
func (c *Client) connectWithRetry(ctx context.Context) error {
	dialer := &websocket.Dialer{
		HandshakeTimeout: c.handshakeTimeout,
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.baseBackoff * time.Duration(1<<(attempt-1))
			c.logger.Info("Retrying connection", "attempt", attempt, "backoff", backoff)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		} else {
			c.logger.Info("Connecting to flashblock WebSocket", "url", c.url)
		}

		conn, resp, err := dialer.DialContext(ctx, c.url, c.headers)
		if err != nil {
			lastErr = err
			if resp != nil {
				c.logger.Warn("WebSocket connection attempt failed", "attempt", attempt+1, "status", resp.Status, "error", err)
				resp.Body.Close()
			} else {
				c.logger.Warn("WebSocket connection attempt failed", "attempt", attempt+1, "error", err)
			}
			continue
		}

		if resp != nil {
			resp.Body.Close()
		}

		c.conn = conn
		c.logger.Info("Connected to flashblock WebSocket successfully", "attempt", attempt+1)
		return nil
	}

	return fmt.Errorf("failed to connect after %d attempts: %w", c.maxRetries+1, lastErr)
}

// Close closes the WebSocket connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ReadMessage reads and decodes a flashblock message from the WebSocket
// Automatically attempts reconnection if the connection is lost or times out
// Waits for connection if not connected
func (c *Client) ReadMessage() (*FlashblocksPayloadV1, error) {
	// If not connected, attempt to connect first
	if c.conn == nil {
		c.logger.Info("Not connected, attempting initial connection")
		ctx, cancel := context.WithTimeout(context.Background(), c.handshakeTimeout*time.Duration(c.maxRetries+1))
		defer cancel()

		if err := c.connectWithRetry(ctx); err != nil {
			return nil, fmt.Errorf("failed to establish initial connection: %w", err)
		}
	}

	// Set read deadline for timeout
	if c.readTimeout > 0 {
		if err := c.conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			return nil, fmt.Errorf("failed to set read deadline: %w", err)
		}
	}

	_, rawMessage, err := c.conn.ReadMessage()
	if err != nil {
		// Check if reconnection should be attempted
		shouldReconnect := false
		isTimeout := false

		// Check for timeout errors
		if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
			c.logger.Warn("Read timeout, attempting reconnection", "timeout", c.readTimeout)
			shouldReconnect = true
			isTimeout = true
		} else if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) ||
			websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
			c.logger.Warn("WebSocket connection lost, attempting reconnection", "error", err)
			shouldReconnect = true
		}

		if shouldReconnect {
			// Close the old connection
			if c.conn != nil {
				c.conn.Close()
				c.conn = nil
			}

			// Attempt to reconnect
			ctx, cancel := context.WithTimeout(context.Background(), c.handshakeTimeout*time.Duration(c.maxRetries+1))
			defer cancel()

			if reconnectErr := c.connectWithRetry(ctx); reconnectErr != nil {
				return nil, fmt.Errorf("failed to reconnect after %s: %w",
					map[bool]string{true: "timeout", false: "connection loss"}[isTimeout],
					reconnectErr)
			}

			c.logger.Info("Successfully reconnected, note: messages may have been missed during reconnection")

			// After reconnection, try reading again immediately
			return c.ReadMessage()
		}

		return nil, fmt.Errorf("failed to read WebSocket message: %w", err)
	}

	return c.decodeMessage(rawMessage)
}

// decodeMessage decodes a raw message (handles both plain JSON and brotli-compressed)
func (c *Client) decodeMessage(rawMessage []byte) (*FlashblocksPayloadV1, error) {
	// Try to decode as JSON first
	var msg FlashblocksPayloadV1
	err := json.Unmarshal(rawMessage, &msg)
	if err != nil {
		// If JSON decode fails, try brotli decompression
		c.logger.Debug("JSON decode failed, attempting brotli decompression")

		reader := brotli.NewReader(bytes.NewReader(rawMessage))
		var buf bytes.Buffer
		_, err = buf.ReadFrom(reader)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress brotli message: %w", err)
		}

		// Try JSON decode on decompressed data
		err = json.Unmarshal(buf.Bytes(), &msg)
		if err != nil {
			return nil, fmt.Errorf("failed to decode decompressed JSON: %w", err)
		}
	}

	return &msg, nil
}

// Listen continuously reads messages from the WebSocket and prints them to the terminal
func (c *Client) Listen(ctx context.Context) error {
	messageCount := 0

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Stopping flashblock listener", "total_messages", messageCount)
			return ctx.Err()
		default:
			msg, err := c.ReadMessage()
			if err != nil {
				return fmt.Errorf("error reading message: %w", err)
			}

			messageCount++
			c.printMessage(msg, messageCount)
		}
	}
}

// printMessage formats and prints a flashblock message to the terminal
func (c *Client) printMessage(msg *FlashblocksPayloadV1, count int) {
	c.logger.Debug("Flashblock received",
		"count", count,
		"payload_id", msg.PayloadID.String(),
		"index", msg.Index,
	)

	if msg.Static != nil {
		c.logger.Trace("  Base",
			"parent_hash", msg.Static.ParentHash.Hex(),
			"fee_recipient", msg.Static.FeeRecipient.Hex(),
			"block_number", uint64(msg.Static.BlockNumber),
			"gas_limit", uint64(msg.Static.GasLimit),
			"timestamp", uint64(msg.Static.Timestamp),
			"base_fee_per_gas", msg.Static.BaseFeePerGas.ToInt(),
		)
	}

	c.logger.Trace("  Diff",
		"state_root", msg.Diff.StateRoot.Hex(),
		"block_hash", msg.Diff.BlockHash.Hex(),
		"gas_used", uint64(msg.Diff.GasUsed),
		"tx_count", len(msg.Diff.Transactions),
	)

	for i, tx := range msg.Diff.Transactions {
		c.logger.Trace("    Transaction", "index", i, "data_len", len(tx))
	}
}
