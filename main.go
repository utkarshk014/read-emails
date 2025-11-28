package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Global in-memory stores
var (
	tokenStore = struct {
		sync.RWMutex
		tokens map[string]*oauth2.Token
	}{tokens: make(map[string]*oauth2.Token)}

	historyStore = struct {
		sync.RWMutex
		history map[string]uint64
	}{history: make(map[string]uint64)}

	oauthConfig *oauth2.Config
)

func main() {
	// Load .env file if it exists (ignore error if file doesn't exist)
	_ = godotenv.Load()

	var err error
	oauthConfig, err = loadConfig()
	if err != nil {
		log.Fatalf("Unable to load OAuth config: %v", err)
	}

	http.HandleFunc("/auth-url", authURLHandler)
	http.HandleFunc("/oauth2/callback", oauth2CallbackHandler)
	http.HandleFunc("/emails/summary", emailSummaryHandler)
	http.HandleFunc("/watch/start", watchStartHandler)
	http.HandleFunc("/gmail/push", gmailPushHandler)

	log.Println("Server started at :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// loadConfig reads credentials.json and builds oauth2.Config
func loadConfig() (*oauth2.Config, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %v", err)
	}

	// Use only gmail.readonly scope - it supports:
	// - Reading full email messages (including body)
	// - Using query parameters (q parameter)
	// - Watch functionality for push notifications
	config, err := google.ConfigFromJSON(b, gmail.GmailReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}

	config.RedirectURL = "http://localhost:8080/oauth2/callback"
	return config, nil
}

// getGmailService creates an authenticated Gmail service client
func getGmailService(ctx context.Context, token *oauth2.Token) (*gmail.Service, error) {
	client := oauthConfig.Client(ctx, token)
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve Gmail client: %v", err)
	}
	return srv, nil
}

// getUserEmail retrieves the user's email address from Gmail profile
func getUserEmail(service *gmail.Service) (string, error) {
	userProfile, err := service.Users.GetProfile("me").Do()
	if err != nil {
		return "", fmt.Errorf("unable to get user profile: %v", err)
	}
	return userProfile.EmailAddress, nil
}

// extractEmailBody extracts the email body text from a Gmail message payload
// Handles both simple and multipart messages (including nested multipart)
func extractEmailBody(payload *gmail.MessagePart) string {
	var plainTextBody, htmlBody string

	// Helper function to recursively extract body from parts
	var extractFromPart func(part *gmail.MessagePart)
	extractFromPart = func(part *gmail.MessagePart) {
		if part == nil {
			return
		}

		// If this part has a body, extract it
		if part.Body != nil && part.Body.Data != "" {
			data, err := base64.URLEncoding.DecodeString(part.Body.Data)
			if err == nil {
				content := string(data)
				switch part.MimeType {
				case "text/plain":
					if plainTextBody == "" {
						plainTextBody = content
					}
				case "text/html":
					if htmlBody == "" {
						htmlBody = content
					}
				}
			}
		}

		// Recursively process nested parts
		if part.Parts != nil {
			for _, subPart := range part.Parts {
				extractFromPart(subPart)
			}
		}
	}

	// Start extraction from the root payload
	extractFromPart(payload)

	// Prefer plain text over HTML
	if plainTextBody != "" {
		return plainTextBody
	}
	return htmlBody
}

// authURLHandler generates and returns the Google OAuth consent URL
func authURLHandler(w http.ResponseWriter, r *http.Request) {
	authURL := oauthConfig.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	log.Printf("Visit the URL for the auth dialog: %v", authURL)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"auth_url": authURL})
}

// oauth2CallbackHandler handles Google redirect after user approves access
func oauth2CallbackHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	token, err := oauthConfig.Exchange(ctx, code)
	if err != nil {
		log.Printf("Unable to retrieve token: %v", err)
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	// Get Gmail service to retrieve user email
	srv, err := getGmailService(ctx, token)
	if err != nil {
		log.Printf("Unable to create Gmail service: %v", err)
		http.Error(w, "Failed to create Gmail service", http.StatusInternalServerError)
		return
	}

	userEmail, err := getUserEmail(srv)
	if err != nil {
		log.Printf("Unable to get user email: %v", err)
		http.Error(w, "Failed to get user email", http.StatusInternalServerError)
		return
	}

	// Store tokens keyed by email
	tokenStore.Lock()
	tokenStore.tokens[userEmail] = token
	tokenStore.Unlock()

	// Log authentication details
	log.Printf("User authenticated: %s", userEmail)
	log.Printf("Access token: %s...", token.AccessToken[:min(20, len(token.AccessToken))])
	if token.RefreshToken != "" {
		log.Printf("Refresh token: present")
	} else {
		log.Printf("Refresh token: not present")
	}
	if token.Expiry.IsZero() {
		log.Printf("Token expiry: not set")
	} else {
		log.Printf("Token expiry: %v", token.Expiry)
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><body><h1>Authentication complete</h1><p>User: %s</p><p>You can return to the backend logs.</p></body></html>", userEmail)
}

// emailSummaryHandler returns count of emails and latest email from last 30 days
func emailSummaryHandler(w http.ResponseWriter, r *http.Request) {
	userEmail := r.URL.Query().Get("userEmail")
	if userEmail == "" {
		http.Error(w, "Missing userEmail parameter", http.StatusBadRequest)
		return
	}

	// Retrieve tokens
	tokenStore.RLock()
	token, exists := tokenStore.tokens[userEmail]
	tokenStore.RUnlock()
	if !exists {
		http.Error(w, "User not authenticated", http.StatusUnauthorized)
		return
	}

	ctx := context.Background()
	srv, err := getGmailService(ctx, token)
	if err != nil {
		log.Printf("Unable to create Gmail service: %v", err)
		http.Error(w, "Failed to create Gmail service", http.StatusInternalServerError)
		return
	}

	// Query emails from last 30 days
	query := "newer_than:30d"
	msgs, err := srv.Users.Messages.List("me").Q(query).MaxResults(500).Do()
	if err != nil {
		log.Printf("Unable to list messages: %v", err)
		http.Error(w, "Failed to list messages", http.StatusInternalServerError)
		return
	}

	// Count emails (use ResultSizeEstimate if available, otherwise count actual results)
	count := int64(0)
	if msgs.ResultSizeEstimate > 0 {
		count = msgs.ResultSizeEstimate
	} else {
		count = int64(len(msgs.Messages))
	}

	var latestEmail map[string]interface{}
	if len(msgs.Messages) > 0 {
		// Get the first (latest) message with full format to read email body
		msgID := msgs.Messages[0].Id
		msg, err := srv.Users.Messages.Get("me", msgID).Format("full").Do()
		if err != nil {
			log.Printf("Unable to get message: %v", err)
			http.Error(w, "Failed to get message", http.StatusInternalServerError)
			return
		}

		// Extract headers
		headers := make(map[string]string)
		for _, h := range msg.Payload.Headers {
			headers[h.Name] = h.Value
		}

		// Extract email body
		body := extractEmailBody(msg.Payload)

		latestEmail = map[string]interface{}{
			"id":      msg.Id,
			"subject": headers["Subject"],
			"from":    headers["From"],
			"date":    headers["Date"],
			"snippet": msg.Snippet,
			"body":    body,
		}
	}

	response := map[string]interface{}{
		"user_email":         userEmail,
		"count_last_30_days": count,
		"latest_email":       latestEmail,
	}

	// Log the summary
	log.Printf("Email summary for %s: count=%d", userEmail, count)
	if latestEmail != nil {
		log.Printf("Latest email: subject=%s, from=%s", latestEmail["subject"], latestEmail["from"])
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// watchStartHandler sets up Gmail watch for push notifications
func watchStartHandler(w http.ResponseWriter, r *http.Request) {
	userEmail := r.URL.Query().Get("userEmail")
	if userEmail == "" {
		http.Error(w, "Missing userEmail parameter", http.StatusBadRequest)
		return
	}

	// Retrieve tokens
	tokenStore.RLock()
	token, exists := tokenStore.tokens[userEmail]
	tokenStore.RUnlock()
	if !exists {
		http.Error(w, "User not authenticated", http.StatusUnauthorized)
		return
	}

	ctx := context.Background()
	srv, err := getGmailService(ctx, token)
	if err != nil {
		log.Printf("Unable to create Gmail service: %v", err)
		http.Error(w, "Failed to create Gmail service", http.StatusInternalServerError)
		return
	}

	// Get project ID from environment or use placeholder
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "YOUR_PROJECT_ID" // User must set this
		log.Printf("Warning: GOOGLE_CLOUD_PROJECT not set, using placeholder")
	}

	topicName := fmt.Sprintf("projects/%s/topics/gmail-notifications", projectID)
	req := &gmail.WatchRequest{
		TopicName: topicName,
		LabelIds:  []string{"INBOX"},
	}

	res, err := srv.Users.Watch("me", req).Do()
	if err != nil {
		log.Printf("Unable to start watch: %v", err)
		http.Error(w, fmt.Sprintf("Failed to start watch: %v", err), http.StatusInternalServerError)
		return
	}

	// Store history ID
	historyStore.Lock()
	historyStore.history[userEmail] = res.HistoryId
	historyStore.Unlock()

	log.Printf("Watch started for user %s: topic=%s, historyId=%d, expiration=%v", userEmail, topicName, res.HistoryId, res.Expiration)

	response := map[string]interface{}{
		"status":     "watch_started",
		"history_id": res.HistoryId,
		"expiration": res.Expiration,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// gmailPushHandler receives Gmail push notifications via Pub/Sub
func gmailPushHandler(w http.ResponseWriter, r *http.Request) {
	// Pub/Sub sends POST requests with JSON body
	var notification struct {
		Message struct {
			Data      string `json:"data"`
			MessageID string `json:"messageId"`
		} `json:"message"`
		Subscription string `json:"subscription"`
	}

	if err := json.NewDecoder(r.Body).Decode(&notification); err != nil {
		log.Printf("Unable to parse push notification: %v", err)
		http.Error(w, "Failed to parse request", http.StatusBadRequest)
		return
	}

	// Decode base64 data
	data, err := base64.StdEncoding.DecodeString(notification.Message.Data)
	if err != nil {
		// Try URL encoding if standard fails
		data, err = base64.URLEncoding.DecodeString(notification.Message.Data)
		if err != nil {
			log.Printf("Unable to decode message data: %v", err)
			http.Error(w, "Failed to decode message data", http.StatusBadRequest)
			return
		}
	}

	// Parse Gmail push notification data
	// Note: Gmail sends historyId as either a number or string in JSON
	var pushDataRaw map[string]interface{}
	if err := json.Unmarshal(data, &pushDataRaw); err != nil {
		log.Printf("Unable to parse push data: %v", err)
		http.Error(w, "Failed to parse push data", http.StatusBadRequest)
		return
	}

	// Extract email address
	emailAddress, ok := pushDataRaw["emailAddress"].(string)
	if !ok {
		log.Printf("Unable to extract emailAddress from push data")
		http.Error(w, "Failed to extract emailAddress", http.StatusBadRequest)
		return
	}

	// Extract historyId - can be number or string
	var historyId uint64
	switch v := pushDataRaw["historyId"].(type) {
	case float64:
		// JSON numbers are unmarshaled as float64
		historyId = uint64(v)
	case string:
		// If it's a string, parse it
		var err error
		historyId, err = strconv.ParseUint(v, 10, 64)
		if err != nil {
			log.Printf("Unable to parse historyId string: %v", err)
			http.Error(w, "Failed to parse historyId", http.StatusBadRequest)
			return
		}
	default:
		log.Printf("Unexpected historyId type: %T", v)
		http.Error(w, "Invalid historyId format", http.StatusBadRequest)
		return
	}

	log.Printf("Received push notification for user: %s, historyId: %d", emailAddress, historyId)

	// Retrieve tokens for this user
	tokenStore.RLock()
	token, exists := tokenStore.tokens[emailAddress]
	tokenStore.RUnlock()
	if !exists {
		log.Printf("User %s not authenticated", emailAddress)
		http.Error(w, "User not authenticated", http.StatusUnauthorized)
		return
	}

	// Get stored history ID
	historyStore.RLock()
	lastHistoryId, hasHistory := historyStore.history[emailAddress]
	historyStore.RUnlock()

	if !hasHistory {
		log.Printf("No stored history ID for user %s, using current historyId", emailAddress)
		lastHistoryId = historyId
	}

	ctx := context.Background()
	srv, err := getGmailService(ctx, token)
	if err != nil {
		log.Printf("Unable to create Gmail service: %v", err)
		http.Error(w, "Failed to create Gmail service", http.StatusInternalServerError)
		return
	}

	// Get history changes
	history, err := srv.Users.History.List("me").StartHistoryId(lastHistoryId).Do()
	if err != nil {
		log.Printf("Unable to get history: %v", err)
		http.Error(w, "Failed to get history", http.StatusInternalServerError)
		return
	}

	// Process new messages
	for _, historyRecord := range history.History {
		for _, messageAdded := range historyRecord.MessagesAdded {
			msgID := messageAdded.Message.Id

			// Get message details with full format to read email body
			msg, err := srv.Users.Messages.Get("me", msgID).Format("full").Do()
			if err != nil {
				log.Printf("Unable to get message %s: %v", msgID, err)
				continue
			}

			// Extract headers
			headers := make(map[string]string)
			for _, h := range msg.Payload.Headers {
				headers[h.Name] = h.Value
			}

			// Extract email body
			body := extractEmailBody(msg.Payload)
			subject := headers["Subject"]

			// Check if this is a credit card transaction email
			if isCreditCardTransactionEmail(subject, body) {
				// Parse credit card transaction details
				txn := parseCreditCardTransaction(subject, body)

				log.Printf("=== CREDIT CARD TRANSACTION DETECTED ===")
				log.Printf("New email received for %s:", emailAddress)
				log.Printf("  Message ID: %s", msg.Id)
				log.Printf("  Subject: %s", subject)
				log.Printf("  From: %s", headers["From"])
				log.Printf("  Date: %s", headers["Date"])
				log.Printf("--- Transaction Details ---")
				log.Printf("  Amount: %s", txn.Amount)
				log.Printf("  Card Number: %s", txn.CardNumber)
				log.Printf("  Merchant: %s", txn.Merchant)
				log.Printf("  Date: %s", txn.Date)
				log.Printf("  Time: %s", txn.Time)
				log.Printf("================================")
			} else {
				// Non-credit card email
				log.Printf("=== NON CREDIT CARD INFO EMAIL ===")
				log.Printf("New email received for %s:", emailAddress)
				log.Printf("  Message ID: %s", msg.Id)
				log.Printf("  Subject: %s", subject)
				log.Printf("  From: %s", headers["From"])
				log.Printf("  Date: %s", headers["Date"])
				log.Printf("  Snippet: %s", msg.Snippet)
				log.Printf("================================")
			}
		}
	}

	// Update stored history ID
	historyStore.Lock()
	historyStore.history[emailAddress] = historyId
	historyStore.Unlock()

	// Return 200 OK to acknowledge receipt
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// CreditCardTransaction represents parsed credit card transaction details
type CreditCardTransaction struct {
	Amount     string
	CardNumber string
	Merchant   string
	Date       string
	Time       string
}

// isCreditCardTransactionEmail checks if an email is a credit card transaction notification
func isCreditCardTransactionEmail(subject, body string) bool {
	// Check for common credit card transaction keywords
	keywords := []string{
		"credit card",
		"debit.*card",
		"card.*ending",
		"card.*\\*\\*",
		"debited.*card",
		"transaction.*card",
	}

	combined := strings.ToLower(subject + " " + body)
	for _, keyword := range keywords {
		matched, _ := regexp.MatchString(keyword, combined)
		if matched {
			return true
		}
	}
	return false
}

// parseCreditCardTransaction extracts transaction details from email subject and body
func parseCreditCardTransaction(subject, body string) *CreditCardTransaction {
	txn := &CreditCardTransaction{}

	// Combine subject and body for parsing
	combined := subject + " " + body

	// Extract amount - patterns like "Rs.424.00", "₹424.00", "$424.00", "INR 424.00"
	amountPattern := regexp.MustCompile(`(?i)(?:Rs\.|₹|INR|USD|\$)\s*([\d,]+\.?\d*)`)
	if matches := amountPattern.FindStringSubmatch(combined); len(matches) > 1 {
		txn.Amount = strings.TrimSpace(matches[1])
	}

	// Extract card number - patterns like "ending 0000", "**0000", "card ending in 0000"
	cardPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:ending|ending in|card ending)\s+(\d{4})`),
		regexp.MustCompile(`(?i)\*\*(\d{4})`),
		regexp.MustCompile(`(?i)card\s+(\d{4})`),
	}
	for _, pattern := range cardPatterns {
		if matches := pattern.FindStringSubmatch(combined); len(matches) > 1 {
			txn.CardNumber = matches[1]
			break
		}
	}

	// Extract merchant - patterns like "towards Swiggy Limited", "at Swiggy", "from Swiggy"
	merchantPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:towards|at|from|with)\s+([A-Za-z][A-Za-z\s&]+?)(?:\s+on|\s+at|\.|$)`),
		regexp.MustCompile(`(?i)(?:merchant|vendor):\s*([A-Za-z][A-Za-z\s&]+?)(?:\s+on|\s+at|\.|$)`),
	}
	for _, pattern := range merchantPatterns {
		if matches := pattern.FindStringSubmatch(combined); len(matches) > 1 {
			merchant := strings.TrimSpace(matches[1])
			// Clean up common suffixes
			merchant = regexp.MustCompile(`(?i)\s+(limited|ltd|inc|corp|corporation)\.?$`).ReplaceAllString(merchant, "")
			txn.Merchant = strings.TrimSpace(merchant)
			if txn.Merchant != "" {
				break
			}
		}
	}

	// Extract date - patterns like "11 Nov, 2025", "11-Nov-2025", "2025-11-11"
	datePatterns := []*regexp.Regexp{
		regexp.MustCompile(`(\d{1,2}\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s*,?\s*\d{4})`),
		regexp.MustCompile(`(\d{1,2}[-/]\d{1,2}[-/]\d{4})`),
		regexp.MustCompile(`(\d{4}[-/]\d{1,2}[-/]\d{1,2})`),
	}
	for _, pattern := range datePatterns {
		if matches := pattern.FindStringSubmatch(combined); len(matches) > 1 {
			txn.Date = strings.TrimSpace(matches[1])
			break
		}
	}

	// Extract time - patterns like "12:38:53", "12:38 PM", "12:38"
	timePattern := regexp.MustCompile(`(\d{1,2}:\d{2}(?::\d{2})?(?:\s*(?:AM|PM))?)`)
	if matches := timePattern.FindStringSubmatch(combined); len(matches) > 1 {
		txn.Time = strings.TrimSpace(matches[1])
	}

	return txn
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
