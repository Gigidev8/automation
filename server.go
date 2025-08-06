package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/dghubble/oauth1"
	"github.com/joho/godotenv"
)

// TelegramUpdate represents an incoming update from Telegram.
type TelegramUpdate struct {
	UpdateID int             `json:"update_id"`
	Message  TelegramMessage `json:"message"`
}

// TelegramMessage represents a message from Telegram.
type TelegramMessage struct {
	Text string `json:"text"`
}

// Article represents the data fetched from the viewon.news API.
type Article struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

func main() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file, using environment variables")
	}

	// Handlers
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "The server is running.")
	})
	http.HandleFunc("/telegram", telegramHandler)

	// Start the server
	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func telegramHandler(w http.ResponseWriter, r *http.Request) {
	var update TelegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("could not decode incoming telegram message: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	log.Printf("Received message from user: %s", update.Message.Text)
	log.Println("[SUCCESS] New message received and decoded.")

	// --- Input Validation to prevent retry loops ---
	if strings.HasPrefix(update.Message.Text, "http://") || strings.HasPrefix(update.Message.Text, "https://") {
		log.Println("[INFO] Received a URL instead of an ID. Ignoring and sending 200 OK to clear Telegram's queue.")
		w.WriteHeader(http.StatusOK) // Send OK to stop Telegram from retrying.
		return
	}
	// --- End of Input Validation ---

	// Fetch the article using the message text as the ID
	article, err := fetchArticle(update.Message.Text)
	if err != nil {
		errMsg := fmt.Sprintf("❌ Failed to fetch article with ID %s. Reason: %v", update.Message.Text, err)
		sendTelegramNotification(errMsg, "") // Send error as plain text
		log.Printf("could not fetch article: %v", err)
		// We've handled the error by notifying. Now tell Telegram we're OK to prevent retries.
		w.WriteHeader(http.StatusOK)
		return
	}
	log.Println("[SUCCESS] Article data fetched.")

	// Get hashtags from OpenRouter
	hashtags, err := getHashtags(article.Title, article.Description)
	if err != nil {
		errMsg := fmt.Sprintf("❌ Failed to get hashtags for article with ID %s. Reason: %v", update.Message.Text, err)
		sendTelegramNotification(errMsg, "") // Send error as plain text
		log.Printf("ERROR: could not get hashtags: %v", err)
		// We've handled the error. Tell Telegram we're OK to prevent retries.
		w.WriteHeader(http.StatusOK)
		return
	}
	log.Println("[SUCCESS] Hashtags generated.")

	// Post the article to Twitter
	if err := postToTwitter(article, update.Message.Text, hashtags); err != nil {
		errMsg := fmt.Sprintf("❌ Failed to post to Twitter for article with ID %s. Reason: %v", update.Message.Text, err)
		sendTelegramNotification(errMsg, "") // Send error as plain text
		log.Printf("could not post to twitter: %v", err)
		// We've handled the error. Tell Telegram we're OK to prevent retries.
		w.WriteHeader(http.StatusOK)
		return
	}
	log.Println("[SUCCESS] Tweet posted to X.")

	w.WriteHeader(http.StatusOK)
	// Send a success notification with Markdown formatting.
	successMessage := fmt.Sprintf("✅ Successfully posted article with ID: `%s`", update.Message.Text)
	sendTelegramNotification(successMessage, "MarkdownV2")
}

// sendTelegramNotification sends a formatted message to a specified Telegram chat.
func sendTelegramNotification(message string, parseMode string) {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if botToken == "" || chatID == "" {
		log.Println("WARNING: TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set. Cannot send notification.")
		return
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	// Use a map for the request body to handle different fields easily.
	requestData := map[string]string{
		"chat_id": chatID,
		"text":    message,
	}
	// Only add parse_mode if it's not empty, otherwise send as plain text.
	if parseMode != "" {
		requestData["parse_mode"] = parseMode
	}

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		log.Printf("WARNING: Failed to marshal notification body: %v", err)
		return
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		log.Printf("WARNING: Failed to send notification to Telegram: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("WARNING: Telegram API returned non-200 status for notification: %s", string(body))
	} else {
		log.Printf("[SUCCESS] Sent notification to Telegram group: %s", message)
	}
}

func fetchArticle(id string) (*Article, error) {
	url := fmt.Sprintf("https://viewon.news/notion.php?id=%s", id)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("could not fetch article: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var article Article
	if err := json.NewDecoder(resp.Body).Decode(&article); err != nil {
		return nil, fmt.Errorf("could not decode article data: %w", err)
	}

	return &article, nil
}

func postToTwitter(article *Article, messageID string, hashtags string) error {
	consumerKey := os.Getenv("TWITTER_CONSUMER_KEY")
	consumerSecret := os.Getenv("TWITTER_CONSUMER_SECRET")
	accessToken := os.Getenv("TWITTER_ACCESS_TOKEN")
	accessSecret := os.Getenv("TWITTER_ACCESS_SECRET")

	if consumerKey == "" || consumerSecret == "" || accessToken == "" || accessSecret == "" {
		return fmt.Errorf("twitter api credentials not set")
	}

	config := oauth1.NewConfig(consumerKey, consumerSecret)
	token := oauth1.NewToken(accessToken, accessSecret)
	// httpClient will automatically sign requests with OAuth 1.0a User Context
	httpClient := config.Client(oauth1.NoContext, token)

	// --- Start of API v2 Implementation ---

	// 1. Construct the article URL
	articleURL := fmt.Sprintf("https://viewon.news/article.html?id=%s", messageID)

	// 2. Set the tweet text
	tweetText := fmt.Sprintf("%s\n%s\n\n%s", article.Title, hashtags, articleURL)

	// 3. Create the JSON payload for the v2 endpoint
	payload := []byte(fmt.Sprintf(`{"text": %q}`, tweetText))

	// 3. The API v2 endpoint for creating a tweet
	tweetURL := "https://api.twitter.com/2/tweets"

	// 4. Create the HTTP request
	req, err := http.NewRequest("POST", tweetURL, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 5. Send the request using the authenticated httpClient
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 6. Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// 7. Check the response status code
	if resp.StatusCode != http.StatusCreated { // A successful v2 tweet creation returns 201 Created
		return fmt.Errorf("received non-201 status code: %d\nResponse: %s", resp.StatusCode, string(body))
	}

	log.Println("[SUCCESS] Tweet posted to X.")
	return nil
}

// Structs for OpenRouter API
type OpenRouterRequest struct {
	Model    string              `json:"model"`
	Messages []OpenRouterMessage `json:"messages"`
}

type OpenRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenRouterResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func getHashtags(title, description string) (string, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENROUTER_API_KEY not set")
	}

	prompt := fmt.Sprintf("Based on the following news article title and description, generate 3-5 relevant hashtags for a tweet. Do not include any other text, just the hashtags starting with #.\n\nTitle: %s\nDescription: %s", title, description)

	requestBody := OpenRouterRequest{
		Model: "deepseek/deepseek-r1:free", // Use the user-specified free model
		Messages: []OpenRouterMessage{
			{Role: "user", Content: prompt},
		},
	}

	payload, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewBuffer(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request to OpenRouter: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from OpenRouter: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("received non-200 status from OpenRouter: %s", string(body))
	}

	var openRouterResponse OpenRouterResponse
	if err := json.Unmarshal(body, &openRouterResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal OpenRouter response: %w", err)
	}

	if len(openRouterResponse.Choices) > 0 {
		return openRouterResponse.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("no content found in OpenRouter response")
}
