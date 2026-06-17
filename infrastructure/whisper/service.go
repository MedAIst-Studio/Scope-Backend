package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
)

type WhisperService struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
}

func NewWhisperService(apiKey string) *WhisperService {
	return &WhisperService{
		httpClient: &http.Client{},
		apiKey:     apiKey,
		baseURL:    "https://api.openai.com/v1/audio/transcriptions",
	}
}

func (s *WhisperService) Transcribe(ctx context.Context, audioBytes []byte, format string) (*domain.TranscriptionResult, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("whisper API key is not configured")
	}

	// 1. Optimize audio using ffmpeg (compress to 64k mp3 to reduce size and fit within 25MB limit)
	optimizedBytes, ext, err := s.optimizeAudio(ctx, audioBytes, format)
	if err != nil {
		return nil, fmt.Errorf("failed to optimize audio: %w", err)
	}

	// 2. Prepare multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file
	filename := fmt.Sprintf("audio%s", ext)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	_, err = part.Write(optimizedBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to write audio to form: %w", err)
	}

	// Add model parameter
	err = writer.WriteField("model", "whisper-1")
	if err != nil {
		return nil, fmt.Errorf("failed to write model field: %w", err)
	}

	// Add language parameter (Defaulting to Turkish based on requirement)
	err = writer.WriteField("language", "tr")
	if err != nil {
		return nil, fmt.Errorf("failed to write language field: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// 3. Send request
	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whisper API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// 4. Parse response
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response JSON: %w", err)
	}

	return &domain.TranscriptionResult{
		Text:     result.Text,
		Language: "tr",
	}, nil
}

// optimizeAudio uses ffmpeg to convert/compress the audio to a 64kbps MP3 format to save bandwidth and fit within API limits.
func (s *WhisperService) optimizeAudio(ctx context.Context, audioBytes []byte, format string) ([]byte, string, error) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "audio_transcribe_*")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir) // cleanup after

	// Write original audio to temp file
	if format == "" || format[0] != '.' {
		format = ".tmp" // fallback extension
	}
	inputPath := filepath.Join(tempDir, "input"+format)
	if err := os.WriteFile(inputPath, audioBytes, 0600); err != nil {
		return nil, "", fmt.Errorf("failed to write input file: %w", err)
	}

	outputPath := filepath.Join(tempDir, "output.mp3")

	// Run ffmpeg
	// -y : overwrite output
	// -i inputPath : input file
	// -ar 16000 : audio sample rate
	// -ac 1 : audio channels (mono)
	// -c:a libmp3lame : use mp3 codec
	// -b:a 64k : audio bitrate
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", inputPath, "-ar", "16000", "-ac", "1", "-c:a", "libmp3lame", "-b:a", "64k", outputPath)
	
	// Capture stderr for debugging if needed
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("ffmpeg compression failed: %w, stderr: %s", err, stderr.String())
	}

	// Read optimized file
	optimizedBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read optimized file: %w", err)
	}

	return optimizedBytes, ".mp3", nil
}
