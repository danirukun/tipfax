package streamelements

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/DaniruKun/tipfax/internal/config"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/securityguy/escpos"
)

const (
	TipsTopic           = "channel.tips"
	TipsModerationTopic = "channel.tips.moderation"
)

type Message struct {
	Type  string `json:"type"`
	Topic string `json:"topic"`
	Nonce string `json:"nonce"`
	Data  any    `json:"data"`
}

type Astro struct {
	cfg     *config.Config
	conn    *websocket.Conn
	printer *escpos.Escpos
}

func NewAstro(cfg *config.Config, printer *escpos.Escpos) *Astro {
	return &Astro{cfg: cfg, printer: printer}
}

func (a *Astro) Connect() error {
	u := url.URL{Scheme: "wss", Host: "astro.streamelements.com", Path: "/"}
	log.Printf("Connecting to %s", u.String())

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("Error connecting:", err)
	}
	log.Println("Connected to Astro")

	a.conn = conn

	return nil
}

func (a *Astro) SubscribeTips() error {
	// Validate token
	if a.cfg.SeJWTToken == "" {
		return fmt.Errorf("SE_JWT_TOKEN is empty or not set")
	}

	// Basic JWT validation (should have 3 parts separated by dots)
	parts := len(a.cfg.SeJWTToken)
	if parts < 50 {
		log.Printf("⚠️  Warning: JWT token seems unusually short (%d chars). This might be invalid.", parts)
	}

	nonce := uuid.New().String()
	subscribeMessage := map[string]any{
		"type":  "subscribe",
		"nonce": nonce,
		"data": map[string]any{
			"topic":      TipsTopic,
			"token":      a.cfg.SeJWTToken,
			"token_type": "jwt",
		},
	}

	// Log subscription attempt (mask token for security)
	tokenPreview := a.cfg.SeJWTToken
	if len(tokenPreview) > 20 {
		tokenPreview = tokenPreview[:10] + "..." + tokenPreview[len(tokenPreview)-10:]
	}
	log.Printf("Subscribing to topic '%s' with nonce '%s' (token length: %d, preview: %s)",
		TipsTopic, nonce, len(a.cfg.SeJWTToken), tokenPreview)
	log.Printf("Subscription message: type=%s, nonce=%s, topic=%s", subscribeMessage["type"], nonce, TipsTopic)

	if err := a.conn.WriteJSON(subscribeMessage); err != nil {
		log.Printf("Error sending subscription message: %v", err)
		return err
	}

	log.Println("Subscription message sent, waiting for response...")

	return nil
}

func (a *Astro) Listen() error {
	for {
		var msg Message
		err := a.conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Error reading message:", err)
			return err
		}

		log.Printf("Received message: %+v", msg)

		// Handle different message types
		switch msg.Type {
		case "welcome":
			if welcomeData, ok := msg.Data.(map[string]any); ok {
				if clientID, ok := welcomeData["client_id"].(string); ok {
					log.Printf("✅ Connected to Astro (client_id: %s)", clientID)
				}
				if welcomeMsg, ok := welcomeData["message"].(string); ok {
					log.Printf("   %s", welcomeMsg)
				}
			}
		case "response":
			log.Printf("Received response: Type=%s, Nonce=%s", msg.Type, msg.Nonce)

			// Parse response data
			if responseData, ok := msg.Data.(map[string]any); ok {
				responseMsg, hasMessage := responseData["message"].(string)

				// Check if this is an error or success
				isError := false
				if hasMessage {
					// Convert to lowercase for case-insensitive matching
					lowerMsg := strings.ToLower(responseMsg)

					// Check for success indicators first
					successKeywords := []string{"success", "subscribed"}
					hasSuccess := false
					for _, keyword := range successKeywords {
						if strings.Contains(lowerMsg, keyword) {
							hasSuccess = true
							break
						}
					}

					// If not a success message, check for error indicators
					if !hasSuccess {
						errorKeywords := []string{"error", "failed", "invalid", "unauthorized", "forbidden", "not found"}
						for _, keyword := range errorKeywords {
							if strings.Contains(lowerMsg, keyword) {
								isError = true
								break
							}
						}
					}

					// Also check for error code or error type fields
					if _, hasErrorCode := responseData["code"]; hasErrorCode {
						isError = true
					}
					if errorType, ok := responseData["type"].(string); ok {
						if errorType == "error" {
							isError = true
						}
					}
				}

				if isError {
					log.Printf("❌ Error response: %s", responseMsg)
					// Check for additional error details
					if errorCode, ok := responseData["code"].(string); ok {
						log.Printf("   Error code: %s", errorCode)
					}
					if errorType, ok := responseData["type"].(string); ok {
						log.Printf("   Error type: %s", errorType)
					}
					log.Printf("   Full response data: %+v", responseData)
				} else {
					// Success response
					if hasMessage {
						log.Printf("✅ %s", responseMsg)
					} else {
						log.Printf("✅ Success response: %+v", responseData)
					}

					// Log topic info if present
					if topic, ok := responseData["topic"].(string); ok {
						log.Printf("   Topic: %s", topic)
					}
					if room, ok := responseData["room"].(string); ok {
						log.Printf("   Room: %s", room)
					}
				}
			} else {
				log.Printf("Response data (raw): %+v", msg.Data)
			}
		case "message":
			log.Println("Received notification:", msg)
			// Process the notification based on the topic
			if msg.Topic == TipsTopic {
				a.handleTipMessage(msg)
			}
		default:
			log.Printf("Received unknown message type '%s': %+v", msg.Type, msg)
		}
	}
}

func (a *Astro) handleTipMessage(msg Message) {
	log.Println("🎉 NEW TIP RECEIVED! 🎉")

	// Parse the tip data
	data, ok := msg.Data.(map[string]any)
	if !ok {
		log.Println("Error parsing tip data")
		return
	}

	if donation, ok := data["donation"].(map[string]any); ok {
		username := "Unknown"
		amount := "0"
		currency := "USD"
		message := ""

		if user, ok := donation["user"].(map[string]any); ok {
			if name, ok := user["username"].(string); ok {
				username = name
			}
		}

		if amt, ok := donation["amount"].(float64); ok {
			amount = fmt.Sprintf("%.2f", amt)
		}
		if curr, ok := donation["currency"].(string); ok {
			currency = curr
		}

		if msg, ok := donation["message"].(string); ok {
			message = msg
		}

		status := "unknown"
		provider := "unknown"
		if statusVal, ok := data["status"].(string); ok {
			status = statusVal
		}
		if providerVal, ok := data["provider"].(string); ok {
			provider = providerVal
		}

		log.Printf("💰 Tip from %s: %s %s (via %s)", username, amount, currency, provider)
		log.Printf("📊 Status: %s", status)
		if message != "" {
			log.Printf("💬 Message: %s", message)
		}

		// Print to thermal printer if available
		if a.printer != nil {
			a.printer.Write(fmt.Sprintf("Tip from %s: %s %s", username, amount, currency))
			a.printer.LineFeed()
			a.printer.Write(fmt.Sprintf("Status: %s", status))
			a.printer.LineFeed()
			if message != "" {
				a.printer.Write(fmt.Sprintf("Message: %s", message))
				a.printer.LineFeed()
			}
			a.printer.PrintAndCut()
		}
	} else {
		log.Println("Error: Could not find donation data in tip message")
		log.Printf("Raw data: %+v", data)
	}
}

func (a *Astro) UnsubscribeTips() error {
	unsubscribeMessage := map[string]any{
		"type":  "unsubscribe",
		"nonce": uuid.New().String(),
		"data": map[string]any{
			"topic":      TipsTopic,
			"token":      a.cfg.SeJWTToken,
			"token_type": "jwt",
		},
	}

	if err := a.conn.WriteJSON(unsubscribeMessage); err != nil {
		log.Println("Error unsubscribing:", err)
	}

	log.Println("Unsubscribed from Astro topic:", TipsTopic)

	return nil
}

func (a *Astro) Disconnect() error {
	log.Println("Disconnecting from Astro")
	return a.conn.Close()
}
