package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

var (
	codexDeviceUserCodeURL  = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL     = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexDeviceVerifyURL    = "https://auth.openai.com/codex/device"
	codexDeviceRedirectURI  = "https://auth.openai.com/deviceauth/callback"
	codexDeviceLoginTimeout = 15 * time.Minute
)

type codexDeviceUserCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
}

type codexDeviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
	CodeChallenge     string `json:"code_challenge"`
}

func (m *Manager) LoginDevice(ctx context.Context, provider config.OAuthProvider, openBrowser bool) (string, error) {
	if provider != ProviderCodex {
		return "", fmt.Errorf("--device is only supported for codex OAuth")
	}
	device, err := requestCodexDeviceUserCode(ctx)
	if err != nil {
		return "", err
	}
	userCode := strings.TrimSpace(firstNonEmpty(device.UserCode, device.UserCodeAlt))
	deviceAuthID := strings.TrimSpace(device.DeviceAuthID)
	if userCode == "" || deviceAuthID == "" {
		return "", fmt.Errorf("codex device flow did not return required fields")
	}
	fmt.Printf("Open this URL to authenticate codex:\n%s\n", codexDeviceVerifyURL)
	fmt.Printf("Enter this code:\n%s\n", userCode)
	if openBrowser {
		cmdName, args := browserCommand(codexDeviceVerifyURL)
		if err := exec.Command(cmdName, args...).Start(); err != nil {
			fmt.Printf("Could not open browser automatically: %v\n", err)
		}
	}
	pollCtx, cancel := context.WithTimeout(ctx, codexDeviceLoginTimeout)
	defer cancel()
	tokenResp, err := pollCodexDeviceToken(pollCtx, deviceAuthID, userCode, parseCodexDevicePollInterval(device.Interval))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && pollCtx.Err() != nil {
			return "", fmt.Errorf("codex device authentication timed out after 15 minutes")
		}
		return "", err
	}
	authCode := strings.TrimSpace(tokenResp.AuthorizationCode)
	codeVerifier := strings.TrimSpace(tokenResp.CodeVerifier)
	codeChallenge := strings.TrimSpace(tokenResp.CodeChallenge)
	if authCode == "" || codeVerifier == "" || codeChallenge == "" {
		return "", fmt.Errorf("codex device flow token response missing required fields")
	}
	token, err := exchangeCodexCode(ctx, authCode, codexDeviceRedirectURI, &PKCE{
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
	})
	if err != nil {
		return "", err
	}
	path, err := m.SaveToken(token)
	if err != nil {
		return "", err
	}
	return path, nil
}

func requestCodexDeviceUserCode(ctx context.Context) (*codexDeviceUserCodeResponse, error) {
	body, err := json.Marshal(map[string]string{"client_id": CodexClientID})
	if err != nil {
		return nil, fmt.Errorf("encode codex device request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceUserCodeURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create codex device request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex device code request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read codex device code response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex device code request failed with status %d", resp.StatusCode)
	}
	var out codexDeviceUserCodeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse codex device code response: %w", err)
	}
	return &out, nil
}

func pollCodexDeviceToken(ctx context.Context, deviceAuthID, userCode string, interval time.Duration) (*codexDeviceTokenResponse, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		body, err := json.Marshal(map[string]string{
			"device_auth_id": deviceAuthID,
			"user_code":      userCode,
		})
		if err != nil {
			return nil, fmt.Errorf("encode codex device poll request: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceTokenURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create codex device poll request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := defaultHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("codex device token poll failed: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read codex device poll response: %w", readErr)
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			var out codexDeviceTokenResponse
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("parse codex device poll response: %w", err)
			}
			return &out, nil
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		default:
			return nil, fmt.Errorf("codex device token polling failed with status %d", resp.StatusCode)
		}
	}
}

func parseCodexDevicePollInterval(raw json.RawMessage) time.Duration {
	if len(raw) == 0 {
		return 5 * time.Second
	}
	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil && asFloat > 0 {
		return time.Duration(asFloat * float64(time.Second))
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(asString), 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	return 5 * time.Second
}
