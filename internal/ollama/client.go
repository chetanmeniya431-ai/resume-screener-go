// Package ollama is a small client for the Ollama HTTP API.
// Ollama runs AI models on our own server, so resumes never leave it.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	chatModel  string
	embedModel string
	http       *http.Client
}

func New(baseURL, chatModel, embedModel string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		chatModel:  chatModel,
		embedModel: embedModel,
		http:       &http.Client{Timeout: timeout},
	}
}

func (c *Client) ChatModel() string  { return c.chatModel }
func (c *Client) EmbedModel() string { return c.embedModel }

// StatusError is a non-2xx reply from Ollama.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("ollama: HTTP %d: %s", e.Code, e.Body) }

// IsPermanent reports errors that will not go away on retry (bad request, unknown model).
// Timeouts, dropped connections, 5xx replies and a bad JSON answer are all worth retrying.
func IsPermanent(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code >= 400 && se.Code < 500 && se.Code != http.StatusTooManyRequests
	}
	return false
}

// DecodeError means the model answered, but not with the JSON we asked for.
type DecodeError struct{ Err error }

func (e *DecodeError) Error() string { return "ollama: model returned invalid JSON: " + e.Err.Error() }
func (e *DecodeError) Unwrap() error { return e.Err }

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		return nil, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	return resp, nil
}

// ── Embeddings ──────────────────────────────────────────────────────────────

// nomic-embed-text works best when documents and questions carry these prefixes.
const (
	DocPrefix   = "search_document: "
	QueryPrefix = "search_query: "
)

// Embed turns texts into vectors in one request.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	resp, err := c.post(ctx, "/api/embed", map[string]any{"model": c.embedModel, "input": inputs})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama: asked for %d embeddings, got %d", len(inputs), len(out.Embeddings))
	}
	return out.Embeddings, nil
}

// ── Chat ────────────────────────────────────────────────────────────────────

type Message struct {
	Role    string `json:"role"` // system, user or assistant
	Content string `json:"content"`
}

// ChatJSON asks the model for an answer that follows a JSON schema and decodes it into out.
// Temperature 0 keeps answers stable for the same resume.
func (c *Client) ChatJSON(ctx context.Context, msgs []Message, schema any, out any) error {
	resp, err := c.post(ctx, "/api/chat", map[string]any{
		"model":    c.chatModel,
		"messages": msgs,
		"stream":   false,
		"format":   schema,
		"options":  map[string]any{"temperature": 0, "num_ctx": 4096},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var reply struct {
		Message Message `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(reply.Message.Content), out); err != nil {
		return &DecodeError{Err: err}
	}
	return nil
}

// ChatStream sends the answer to onToken piece by piece as the model writes it.
func (c *Client) ChatStream(ctx context.Context, msgs []Message, onToken func(string) error) error {
	resp, err := c.post(ctx, "/api/chat", map[string]any{
		"model":    c.chatModel,
		"messages": msgs,
		"stream":   true,
		// num_predict caps the answer length: short answers, and much faster on CPU.
		"options": map[string]any{"temperature": 0.2, "num_ctx": 4096, "num_predict": 300},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var part struct {
			Message Message `json:"message"`
			Done    bool    `json:"done"`
			Error   string  `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &part); err != nil {
			continue
		}
		if part.Error != "" {
			return errors.New("ollama: " + part.Error)
		}
		if part.Message.Content != "" {
			if err := onToken(part.Message.Content); err != nil {
				return err
			}
		}
		if part.Done {
			return nil
		}
	}
	return sc.Err()
}

// ── Health and models ───────────────────────────────────────────────────────

func (c *Client) models(ctx context.Context) (map[string]bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode}
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, m := range out.Models {
		have[m.Name] = true
		have[strings.TrimSuffix(m.Name, ":latest")] = true
	}
	return have, nil
}

// Ping checks that Ollama answers.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.models(ctx)
	return err
}

// MissingModels lists configured models that Ollama has not downloaded yet.
func (c *Client) MissingModels(ctx context.Context) ([]string, error) {
	have, err := c.models(ctx)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, m := range []string{c.chatModel, c.embedModel} {
		if !have[m] && !have[strings.TrimSuffix(m, ":latest")] {
			missing = append(missing, m)
		}
	}
	return missing, nil
}

// Pull downloads a model. It can take minutes the first time.
func (c *Client) Pull(ctx context.Context, model string) error {
	pullCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"model": model, "stream": false})
	req, err := http.NewRequestWithContext(pullCtx, http.MethodPost, c.baseURL+"/api/pull", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req) // no client timeout: large download
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return &StatusError{Code: resp.StatusCode, Body: string(msg)}
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}
