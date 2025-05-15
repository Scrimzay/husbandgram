package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

const (
	elevenLabsvoiceID = "EXAVITQu4vr4xnSDxMaL"
)

var elevenLabsAPIURL = fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s?output_format=mp3_44100_128", elevenLabsvoiceID)
var deepseekAPIURL = "https://api.deepseek.com/chat/completions"
// Global zap logger
var logger *zap.SugaredLogger

func init() {
	// Initialize zap logger
	zapLogger, err := zap.NewProduction() // Use NewDevelopment() for human-readable logs in dev
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize zap logger: %v\n", err)
		os.Exit(1)
	}
	logger = zapLogger.Sugar()

	err = godotenv.Load(".env")
	if err != nil {
		log.Fatalf("Error loading .env file")
	}
}
	
func main() {
	//flush logger on exit
	defer logger.Sync()
	
	cleanupHelperFunc()

	r := gin.Default()
	r.LoadHTMLGlob("*.html")
	r.Static("/audio", "./audio")

	r.GET("/", indexHandler)
	r.POST("/create", logicAPIHandler)

	err := r.Run(":4000")
	if err != nil {
		log.Fatal(err)
	}
}
	
func indexHandler(c *gin.Context) {
	http.ServeFile(c.Writer, c.Request, "index.html")
}

// shared HTTP client with timeout
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

type Message struct {
	Role string `json:"role"`
	Content string `json:"content"`
}

type RequestPayload struct {
	Model string `json:"model"`
	Messages []Message `json:"messages"`
	Stream bool `json:"stream"`
}

type ResponsePayload struct {
	Choices []struct {
		Message struct {
			Role string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func logicAPIHandler(c *gin.Context) {
	// Load API keys from environment variables
	deepseekAPIKey := os.Getenv("DEEPSEEK_API_KEY")
	elevenLabsAPIKey := os.Getenv("ELEVENLABS_API_KEY")
	if deepseekAPIKey == "" || elevenLabsAPIKey == "" {
		logger.Error("Missing API keys")
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Server configuration error</div>`))
		return
	}

	// Get form inputs
	userInput := c.PostForm("userInput")
	genreInput := c.PostForm("genreInput")

	// Validate inputs
	if userInput == "" || genreInput == "" {
		logger.Warn("Invalid input: userInput or genreInput empty")
		c.Data(http.StatusBadRequest, "text/html", []byte(`<div class="text-red-600">userInput and genreInput are required</div>`))
		return
	}
	if !utf8.ValidString(userInput) || !utf8.ValidString(genreInput) {
		logger.Warn("Invalid input: userInput or genreInput contains invalid UTF-8")
		c.Data(http.StatusBadRequest, "text/html", []byte(`<div class="text-red-600">Inputs must contain valid characters</div>`))
		return
	}

	// first hit up deepseek for the conversion
	deepseekPayload := RequestPayload{
		Model: "deepseek-chat",
		Messages: []Message{
			{
				Role: "system",
				Content: fmt.Sprintf("You take my input and return it as if a novelist wrote it and from a first person perspective of a female being told the prompt by a male but the female never responds (excluding any roleplay dialogue and including the input) for this genre: %s . Please keep the responses short, but getting the point across", genreInput),
			},
			{
				Role: "user",
				Content: userInput,
			},
		},
		Stream: false,
	}

	deepseekPayloadBytes, err := json.Marshal(deepseekPayload)
	if err != nil {
		logger.Errorw("Failed to marshal DeepSeek JSON", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to process request</div>`))
		return
	}

	deepseekReq, err := http.NewRequest("POST", deepseekAPIURL, bytes.NewBuffer(deepseekPayloadBytes))
	if err != nil {
		logger.Errorw("Failed to create DeepSeek request", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to process request</div>`))
		return
	}

	deepseekReq.Header.Set("Content-Type", "application/json")
	deepseekReq.Header.Set("Authorization", "Bearer "+deepseekAPIKey)

	deepseekResp, err := httpClient.Do(deepseekReq)
	if err != nil {
		logger.Errorw("Failed to send DeepSeek request", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to contact DeepSeek API</div>`))
		return
	}
	defer deepseekResp.Body.Close()

	if deepseekResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(deepseekResp.Body)
		logger.Errorw("DeepSeek API error", "status", deepseekResp.Status, "body", string(body))
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">DeepSeek API error</div>`))
		return
	}

	deepseekBody, err := io.ReadAll(deepseekResp.Body)
	if err != nil {
		logger.Errorw("Failed to read DeepSeek response", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to process DeepSeek response</div>`))
		return
	}

	var deepseekResponse ResponsePayload
	if err := json.Unmarshal(deepseekBody, &deepseekResponse); err != nil {
		logger.Errorw("Failed to parse DeepSeek JSON", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to parse DeepSeek response</div>`))
		return
	}

	var deepseekAnswerToInput string
	if len(deepseekResponse.Choices) > 0 {
		deepseekAnswerToInput = deepseekResponse.Choices[0].Message.Content
		logger.Infow("DeepSeek response received", "text", deepseekAnswerToInput)
	} else {
		logger.Warn("No choices in DeepSeek response")
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">No response from DeepSeek</div>`))
		return
	}

	// Step 2: Call ElevenLabs API
	elevenLabsPayload := map[string]interface{}{
		"text":      deepseekAnswerToInput,
		"model_id":  "eleven_monolingual_v1",
		"voice_settings": map[string]float64{
			"stability":        0.5,
			"similarity_boost": 0.5,
		},
	}

	elevenLabsPayloadBytes, err := json.Marshal(elevenLabsPayload)
	if err != nil {
		logger.Errorw("Failed to marshal ElevenLabs JSON", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to process request</div>`))
		return
	}

	elevenLabsReq, err := http.NewRequest("POST", elevenLabsAPIURL, bytes.NewBuffer(elevenLabsPayloadBytes))
	if err != nil {
		logger.Errorw("Failed to create ElevenLabs request", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to process request</div>`))
		return
	}

	elevenLabsReq.Header.Set("xi-api-key", elevenLabsAPIKey)
	elevenLabsReq.Header.Set("Content-Type", "application/json")
	elevenLabsReq.Header.Set("accept", "audio/mpeg")

	elevenLabsResp, err := httpClient.Do(elevenLabsReq)
	if err != nil {
		logger.Errorw("Failed to send ElevenLabs request", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to contact ElevenLabs API</div>`))
		return
	}
	defer elevenLabsResp.Body.Close()

	if elevenLabsResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(elevenLabsResp.Body)
		logger.Errorw("ElevenLabs API error", "status", elevenLabsResp.Status, "body", string(body))
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">ElevenLabs API error</div>`))
		return
	}

	// Stream audio to client with HTML wrapper for HTMX
	c.Header("Content-Type", "text/html")
	uniqueID := uuid.New().String()
	audioURL := fmt.Sprintf("/audio/%s", uniqueID)

	// Save audio temporarily to serve via a new endpoint
	tempFile := filepath.Join("audio", uniqueID+".mp3")
	if err := os.MkdirAll("audio", 0755); err != nil {
		logger.Errorw("Failed to create audio directory", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to save audio</div>`))
		return
	}
	outFile, err := os.OpenFile(tempFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		logger.Errorw("Failed to create audio file", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to save audio</div>`))
		return
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, elevenLabsResp.Body); err != nil {
		logger.Errorw("Failed to save audio file", "error", err)
		c.Data(http.StatusInternalServerError, "text/html", []byte(`<div class="text-red-600">Failed to save audio</div>`))
		return
	}

	// Return HTML with audio player
	audioHTML := fmt.Sprintf(`
		<div class="space-y-4 p-4 bg-gray-50 rounded-lg">
			<div class="text-gray-700 mb-2">
				<span class="font-medium">Generated Text:</span>
				<p class="mt-1">%s</p>
			</div>
			<div class="audio-player bg-white p-3 rounded-lg shadow-sm border border-gray-200">
				<audio controls class="w-full" onerror="this.parentElement.innerHTML='<p class=\\'text-red-500\\'>Failed to load audio</p>'">
					<source src="%s.mp3" type="audio/mpeg">
					Your browser does not support the audio element.
				</audio>
			</div>
			<div class="text-sm text-gray-500 mt-2">
				<i class="fas fa-info-circle mr-1"></i> Audio will be automatically deleted after 24 hours
			</div>
		</div>
	`, htmlEscape(deepseekAnswerToInput), audioURL)

	c.Data(http.StatusOK, "text/html", []byte(audioHTML))

	logger.Info("Audio response sent to client")
}

// htmlEscape escapes special characters to prevent XSS
func htmlEscape(s string) string {
	return html.EscapeString(s)
}

func cleanupHelperFunc() {
	// Start concurrent cleanup goroutine
	go func() {
		// Check every hour for old audio files
		for range time.Tick(1 * time.Hour) {
			logger.Info("Running audio file cleanup")
			files, err := os.ReadDir("audio")
			if err != nil {
				logger.Errorw("Failed to read audio directory", "error", err)
				continue
			}
			for _, f := range files {
				// Get file info to access ModTime
				info, err := f.Info()
				if err != nil {
					logger.Errorw("Failed to get file info", "file", f.Name(), "error", err)
					continue
				}
				// ModTime() returns the file's last modification time (from os.FileInfo)
				if time.Since(info.ModTime()) > 1*time.Hour {
					filePath := filepath.Join("audio", f.Name())
					if err := os.Remove(filePath); err != nil {
						logger.Errorw("Failed to delete old audio file", "file", filePath, "error", err)
					} else {
						logger.Infow("Deleted old audio file", "file", filePath)
					}
				}
			}
		}
	}()
}